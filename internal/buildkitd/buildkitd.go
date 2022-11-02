package buildkitd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/mitchellh/go-homedir"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer" // import the docker connection driver
	_ "github.com/moby/buildkit/client/connhelper/kubepod"         // import the kubernetes connection driver
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer" // import the podman connection driver
)

// TODO: change all the buildkit comments+vars to daggerEngine or something

const (
	// TODO: what if different versions of clients run at the same time? Guess that's a pre-existing issue
	containerName = "dagger-engine"
	volumeName    = "dagger-engine"

	engineLockPath = "~/.config/dagger/.engine.lock"
	// Long timeout to allow for slow image pulls of
	// buildkitd while not blocking for infinity
	lockTimeout = 10 * time.Minute
)

func Client(ctx context.Context, imageRef string) (*bkclient.Client, error) {
	// TODO: The fact that both DAGGER_HOST and BUILDKIT_HOST exist is confusing, clean up somehow
	host := os.Getenv("BUILDKIT_HOST")
	if host == "" {
		h, err := startBuildkitdVersion(ctx, imageRef)
		if err != nil {
			return nil, err
		}

		host = h
	}
	opts := []bkclient.ClientOpt{
		bkclient.WithFailFast(),
		bkclient.WithTracerProvider(otel.GetTracerProvider()),
	}

	exp, err := detect.Exporter()
	if err != nil {
		return nil, err
	}

	if td, ok := exp.(bkclient.TracerDelegate); ok {
		opts = append(opts, bkclient.WithTracerDelegate(td))
	}

	c, err := bkclient.New(ctx, host, opts...)
	if err != nil {
		return nil, fmt.Errorf("buildkit client: %w", err)
	}
	return c, nil
}

func startBuildkitdVersion(ctx context.Context, imageRef string) (string, error) {
	if imageRef == "" {
		return "", errors.New("buildkitd image ref is empty")
	}

	if err := checkBuildkit(ctx, imageRef); err != nil {
		return "", err
	}

	return fmt.Sprintf("docker-container://%s", containerName), nil
}

// ensure the buildkit is active and properly set up (e.g. connected to host and last version with moby/buildkit)
func checkBuildkit(ctx context.Context, imageRef string) error {
	// acquire a file-based lock to ensure parallel dagger clients
	// don't interfere with checking+creating the buildkitd container
	lockFilePath, err := homedir.Expand(engineLockPath)
	if err != nil {
		return fmt.Errorf("unable to expand buildkitd lock path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockFilePath), 0755); err != nil {
		return fmt.Errorf("unable to create buildkitd lock path parent dir: %w", err)
	}
	lock := flock.New(lockFilePath)
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("failed to lock buildkitd lock file: %w", err)
	}
	if !locked {
		return fmt.Errorf("failed to acquire buildkitd lock file")
	}
	defer lock.Unlock()

	// check status of buildkitd container
	config, err := getBuildkitInformation(ctx)
	if err != nil {
		// If that failed, it might be because the docker CLI is out of service.
		if err := checkDocker(ctx); err != nil {
			return err
		}

		fmt.Fprintln(os.Stderr, "No buildkitd container found, creating one...")

		removeBuildkit(ctx)
		if err := installBuildkit(ctx, imageRef); err != nil {
			return err
		}
		return nil
	}

	if config.Image != imageRef {
		fmt.Fprintln(os.Stderr, "Buildkitd container is out of date, updating it...")

		if err := removeBuildkit(ctx); err != nil {
			return err
		}
		if err := installBuildkit(ctx, imageRef); err != nil {
			return err
		}
	}
	if !config.IsActive {
		fmt.Fprintln(os.Stderr, "Buildkitd container is not running, starting it...")

		if err := startBuildkit(ctx); err != nil {
			return err
		}
	}

	return nil
}

// ensure the docker CLI is available and properly set up (e.g. permissions to
// communicate with the daemon, etc)
func checkDocker(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "info")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.
			Ctx(ctx).
			Error().
			Err(err).
			Bytes("output", output).
			Msg("failed to run docker")
		return fmt.Errorf("%s%s", err, output)
	}

	return nil
}

// Start the buildkit daemon
func startBuildkit(ctx context.Context) error {
	cmd := exec.CommandContext(ctx,
		"docker",
		"start",
		containerName,
	)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	return waitBuildkit(ctx)
}

// Pull and run the buildkit daemon with a proper configuration
// If the buildkit daemon is already configured, use startBuildkit
func installBuildkit(ctx context.Context, ref string) error {
	// #nosec
	cmd := exec.CommandContext(ctx, "docker", "pull", ref)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// TODO: instead of ignoring error, if sha is used and it exists, use that
		// return fmt.Errorf("docker pull: %w: %s", err, output)
		fmt.Fprintf(os.Stderr, "docker pull: %v: %s\n", err, output)
	}

	// #nosec G204
	cmd = exec.CommandContext(ctx,
		"docker",
		"run",
		"-d",
		"--restart", "always",
		"-v", volumeName+":/var/lib/buildkit",
		"--name", containerName,
		"--privileged",
		ref,
		"--debug",
	)
	output, err = cmd.CombinedOutput()
	if err != nil {
		// If the daemon failed to start because it's already running,
		// chances are another dagger instance started it. We can just ignore
		// the error.
		if !strings.Contains(string(output), "Error response from daemon: Conflict.") {
			return fmt.Errorf("docker run: %w: %s", err, output)
		}
	}
	return waitBuildkit(ctx)
}

// waitBuildkit waits for the buildkit daemon to be responsive.
func waitBuildkit(ctx context.Context) error {
	c, err := bkclient.New(ctx, "docker-container://"+containerName)
	if err != nil {
		return fmt.Errorf("buildkit client: %w", err)
	}

	// FIXME Does output "failed to wait: signal: broken pipe"
	defer c.Close()

	// Try to connect every 100ms up to 100 times (10 seconds total)
	const (
		retryPeriod   = 100 * time.Millisecond
		retryAttempts = 100
	)

	for retry := 0; retry < retryAttempts; retry++ {
		_, err = c.ListWorkers(ctx)
		if err == nil {
			return nil
		}
		time.Sleep(retryPeriod)
	}
	return errors.New("buildkit failed to respond")
}

func removeBuildkit(ctx context.Context) error {
	cmd := exec.CommandContext(ctx,
		"docker",
		"rm",
		"-fv",
		containerName,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove buildkit: %w: %s", err, output)
	}

	return nil
}

func getBuildkitInformation(ctx context.Context) (*buildkitInformation, error) {
	formatString := "{{.Config.Image}};{{.State.Running}};{{if index .NetworkSettings.Networks \"host\"}}{{\"true\"}}{{else}}{{\"false\"}}{{end}}"
	cmd := exec.CommandContext(ctx,
		"docker",
		"inspect",
		"--format",
		formatString,
		containerName,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	s := strings.Split(string(output), ";")

	// Retrieve the image name
	imageRef := strings.TrimSpace(s[0])

	// Retrieve the state
	isActive, err := strconv.ParseBool(strings.TrimSpace(s[1]))
	if err != nil {
		return nil, err
	}

	return &buildkitInformation{
		Image:    imageRef,
		IsActive: isActive,
	}, nil
}

type buildkitInformation struct {
	Image    string
	IsActive bool
}
