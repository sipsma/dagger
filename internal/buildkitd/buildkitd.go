package buildkitd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	image         = "daggerd"
	containerName = "daggerd"
	volumeName    = "daggerd"

	daggerdLockPath = "~/.config/dagger/.daggerd.lock"
	// Long timeout to allow for slow image build of
	// daggerd while not blocking for infinity
	lockTimeout = 10 * time.Minute
)

func Client(ctx context.Context) (*bkclient.Client, error) {
	host := os.Getenv("BUILDKIT_HOST")
	if host == "" {
		h, err := InitDaggerd(ctx)
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

// ensure that daggerd is built, active and properly set up (e.g. connected to host)
// TODO: check update on build version
func InitDaggerd(ctx context.Context) (string, error) {
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

	provisioner, err := initProvisioner(ctx)
	if err != nil {
		return "", err
	}

	fmt.Println("Removing Daggerd container...")
	if err := provisioner.RemoveDaggerd(ctx); err != nil {
		return "", err
	}
	fmt.Println("Updating Daggerd container...")
	if err := provisioner.BuildDaggerd(ctx); err != nil {
		return "", err
	}
	fmt.Println("Provisioning Daggerd container...")
	if err := provisioner.ServeDaggerd(ctx); err != nil {
		return "", err
	}
	return provisioner.host, nil
}

func initProvisioner(ctx context.Context) (*Docker, error) {
	// If that failed, it might be because the docker CLI is out of service.
	if err := checkDocker(ctx); err == nil {
		return &Docker{
			host: fmt.Sprintf("docker-container://%s", containerName),
		}, nil
	}
	return nil, fmt.Errorf("no provisioner available")
}

type Provisioner interface {
	RemoveDaggerd(ctx context.Context) error
	InstallDaggerd(ctx context.Context) error
}
