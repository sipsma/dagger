package buildkitd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/mitchellh/go-homedir"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/tracing/detect"
	"go.dagger.io/dagger/internal/version"
	"go.opentelemetry.io/otel"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer" // import the docker connection driver
	_ "github.com/moby/buildkit/client/connhelper/kubepod"         // import the kubernetes connection driver
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer" // import the podman connection driver
)

const (
	image         = "daggerd"
	containerName = "daggerd"
	volumeName    = "daggerd"

	daggerdLockPath    = "~/.config/dagger/.daggerd.lock"
	shortCommitHashLen = 9
	// Long timeout to allow for slow image build of
	// daggerd while not blocking for infinity
	lockTimeout = 10 * time.Minute
)

func Client(ctx context.Context) (*bkclient.Client, error) {
	host := os.Getenv("DAGGERD_HOST")
	if host == "" {
		h, err := StartBuildInfoDaggerd(ctx)
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

// Workaround the fact that debug.ReadBuildInfo doesn't work in tests:
// https://github.com/golang/go/issues/33976
func StartGoModDaggerd(ctx context.Context) (string, error) {
	// Hack: if in CI, do not check for a local cloak binary
	// As it is always reset between runs
	if value := os.Getenv("GITHUB_ACTION"); value != "" {
		return startDaggerdVersion(ctx, "ci")
	}

	// In dev mode, check the vcs git hash from the cloak binary found in PATH
	path, err := exec.LookPath("cloak")
	if err != nil {
		return "", err
	}

	// Checks the version of the cloak binary found in PATH
	out, err := exec.Command("go", "version", "-m", path).CombinedOutput()
	if err != nil {
		return "", err
	}

	version := ""
	for _, line := range strings.Split(string(out), "\n") {
		// `go version -m cloak` outputs the `vcs.revision` of the cloak binary in $PATH (on host)
		// e.g. `vcs.revision=1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b`
		if strings.Contains(line, "vcs.revision") {
			// Keep the first 9 characters of the revision, as it is the length of the short commit hash, in git
			// len used to tag the image on build
			if len(line) > shortCommitHashLen {
				version = strings.Split(line, "=")[1][:shortCommitHashLen]
			} else {
				return "", fmt.Errorf("unexpected go version output: %s", line)
			}
		}
	}
	return startDaggerdVersion(ctx, version)
}

func StartBuildInfoDaggerd(ctx context.Context) (string, error) {
	vendoredVersion, err := version.Revision()
	if err != nil {
		return "", err
	}

	return startDaggerdVersion(ctx, vendoredVersion)
}

func startDaggerdVersion(ctx context.Context, version string) (string, error) {
	if version == "" {
		return "", errors.New("daggerd version is empty")
	}

	containerName, err := checkDaggerd(ctx, version)
	if err != nil {
		return "", err
	}

	return containerName, nil
}

// ensure that daggerd is built, active and properly set up (e.g. connected to host)
func checkDaggerd(ctx context.Context, version string) (string, error) {
	// acquire a file-based lock to ensure parallel dagger clients
	// don't interfere with checking+creating the daggerd container
	lockFilePath, err := homedir.Expand(daggerdLockPath)
	if err != nil {
		return "", fmt.Errorf("unable to expand daggerd lock path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(lockFilePath), 0755); err != nil {
		return "", fmt.Errorf("unable to create daggerd lock path parent dir: %w", err)
	}
	lock := flock.New(lockFilePath)
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return "", fmt.Errorf("failed to lock daggerd lock file: %w", err)
	}
	if !locked {
		return "", fmt.Errorf("failed to acquire daggerd lock file")
	}
	defer lock.Unlock()

	// Check available provisioner
	provisioner, err := initProvisioner(ctx)
	if err != nil {
		return "", err
	}

	// check status of daggerd
	host, config, err := provisioner.DaggerdState(ctx)
	if err != nil {
		fmt.Println("No daggerd container found, creating one...")

		provisioner.RemoveDaggerd(ctx)

		if err := provisioner.InstallDaggerd(ctx, version); err != nil {
			return "", err
		}
		return host, nil
	}

	if config.Version != version {
		fmt.Println("Daggerd container is out of date, updating it...")

		if err := provisioner.RemoveDaggerd(ctx); err != nil {
			return "", err
		}
		if err := provisioner.InstallDaggerd(ctx, version); err != nil {
			return "", err
		}
	}
	if !config.IsActive {
		fmt.Println("Daggerd container is not running, starting it...")

		if err := provisioner.StartDaggerd(ctx); err != nil {
			return "", err
		}
	}
	return host, nil
}

func initProvisioner(ctx context.Context) (Provisioner, error) {
	// If that failed, it might be because the docker CLI is out of service.
	if err := checkDocker(ctx); err == nil {
		return Docker{
			host: fmt.Sprintf("docker-container://%s", containerName),
		}, nil
	}
	return nil, fmt.Errorf("no provisioner available")
}

type Provisioner interface {
	RemoveDaggerd(ctx context.Context) error
	InstallDaggerd(ctx context.Context, version string) error
	StartDaggerd(ctx context.Context) error
	DaggerdState(ctx context.Context) (string, *daggerdInfo, error)
}

type daggerdInfo struct {
	Version  string
	IsActive bool
}
