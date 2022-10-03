package buildkitd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/mitchellh/go-homedir"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/tracing/detect"
	"go.opentelemetry.io/otel"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer" // import the docker connection driver
	_ "github.com/moby/buildkit/client/connhelper/kubepod"         // import the kubernetes connection driver
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer" // import the podman connection driver
)

const (
	image                = "daggerd"
	containerName        = "daggerd"
	volumeName           = "daggerd"
	dockerfileBuildStage = "daggerd"

	daggerdLockPath = "~/.config/dagger/.daggerd.lock"
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
	// Needs to have the cloak dev binary inside PATH
	path, err := exec.LookPath("cloak")
	if err != nil {
		return "", err
	}

	// Checks the version of the cloak binary from the cloak binary executing the tests
	out, err := exec.Command("go", "version", "-m", path).CombinedOutput()
	if err != nil {
		return "", err
	}

	version := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "vcs.revision") {
			if len(line) > 9 {
				version = strings.Split(line, "=")[1][:9]
			} else {
				return "", fmt.Errorf("unexpected go version output: %s", line)
			}
		}
	}
	return startDaggerdVersion(ctx, version)
}

func StartBuildInfoDaggerd(ctx context.Context) (string, error) {
	vendoredVersion, err := revision()
	if err != nil {
		return "", err
	}

	return startDaggerdVersion(ctx, vendoredVersion)
}

func startDaggerdVersion(ctx context.Context, version string) (string, error) {
	if version == "" {
		return "", errors.New("buildkitd version is empty")
	}

	containerName, err := checkDaggerd(ctx, version)
	if err != nil {
		return "", err
	}

	return containerName, nil
}

// ensure that daggerd is built, active and properly set up (e.g. connected to host)
// TODO: check update on build version
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
	config, err := provisioner.GetBuildkitInformation(ctx)
	if err != nil {
		fmt.Println("No daggerd container found, creating one...")

		provisioner.RemoveDaggerd(ctx)

		if err := provisioner.InstallDaggerd(ctx, version); err != nil {
			return "", err
		}
		return provisioner.GetHost(), nil
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
	return provisioner.GetHost(), nil
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

// TODO: move to own package (duplicate with version.go)
// revision returns the VCS revision being used to build or empty string
// if none.
func revision() (string, error) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("unable to read build info")
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value[:9], nil
		}
	}
	return "", nil
}

type Provisioner interface {
	RemoveDaggerd(ctx context.Context) error
	InstallDaggerd(ctx context.Context, version string) error
	StartDaggerd(ctx context.Context) error
	GetBuildkitInformation(ctx context.Context) (*buildkitInformation, error)
	GetHost() string
}

type buildkitInformation struct {
	Version  string
	IsActive bool
}
