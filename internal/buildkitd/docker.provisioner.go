package buildkitd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "embed"

	bkclient "github.com/moby/buildkit/client"
	"github.com/rs/zerolog/log"
	"go.dagger.io/dagger"
)

const (
	dockerfileName = "Dockerfile.daggerd"
)

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

type Docker struct {
	host string
}

// create a copy of an embed directory
func copyDir(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, path), 0755)
		}

		// #nosec
		srcFile, err := src.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		// #nosec
		dstFile, err := os.Create(filepath.Join(dst, path))
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func (Docker) BuildDaggerd(ctx context.Context) error {
	dirPath, err := os.MkdirTemp(os.TempDir(), "daggerd")
	if err != nil {
		return err
	}

	defer os.RemoveAll(dirPath)
	if err := copyDir(dagger.SourceCode, dirPath); err != nil {
		return err
	}

	fmt.Println("Building daggerd image...")

	// #nosec
	// Workaround to avoid:
	// failed to solve with frontend dockerfile.v0: failed to read dockerfile: Dockerfile.daggerd: no such file or directory
	// Manually create "Dockerfile" from "Dockerfile.daggerd"
	cmd := exec.CommandContext(ctx,
		"cp",
		dirPath+"/"+dockerfileName,
		dirPath+"/"+"Dockerfile",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp error: %s\noutput:%s", err, output)
	}

	// #nosec
	// move to build operation
	cmd = exec.CommandContext(ctx,
		"docker",
		"build",
		"-t",
		image,
		dirPath,
	)

	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build error: %s\noutput:%s", err, output)
	}
	return nil
}

func (Docker) RemoveDaggerd(ctx context.Context) error {
	cmd := exec.CommandContext(ctx,
		"docker",
		"rm",
		"-fv",
		containerName,
	)
	_, err := cmd.CombinedOutput()
	if err != nil {
		return err
	}

	return nil
}

// Pull and run the buildkit daemon with a proper configuration
func (d Docker) ServeDaggerd(ctx context.Context) error {
	// #nosec G204
	cmd := exec.CommandContext(ctx,
		"docker",
		"run",
		"-d",
		"--restart", "always",
		"-v", volumeName+":/var/lib/buildkit",
		"--name", containerName,
		"--privileged",
		image,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If the daemon failed to start because it's already running,
		// chances are another dagger instance started it. We can just ignore
		// the error.
		if !strings.Contains(string(output), "Error response from daemon: Conflict.") {
			return err
		}
	}
	return d.waitDaggerd(ctx)
}

// waitBuildkit waits for the buildkit daemon to be responsive.
func (Docker) waitDaggerd(ctx context.Context) error {
	c, err := bkclient.New(ctx, "docker-container://"+containerName)
	if err != nil {
		return err
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
