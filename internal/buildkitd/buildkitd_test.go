//go:build buildkitd

package buildkitd

import (
	"context"
	"fmt"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func RemoveDaggerdImage(ctx context.Context) error {
	cmd := exec.CommandContext(ctx,
		"docker",
		"rmi",
		"-f",
		image,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove error: %s\noutput:%s", err, output)
	}

	return nil
}

func StopDaggerd(ctx context.Context) error {
	cmd := exec.CommandContext(ctx,
		"docker",
		"stop",
		containerName,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove error: %s\noutput:%s", err, output)
	}

	return nil
}

func TestCheckDaggerd(t *testing.T) {
	ctx := context.Background()

	provisioner, err := initProvisioner(ctx)
	require.NoError(t, err)

	t.Run("no image", func(t *testing.T) {
		// Remove Dagger container
		err := provisioner.RemoveDaggerd(ctx)
		require.NoError(t, err)

		// Remove Dagger image
		err = RemoveDaggerdImage(ctx)
		require.NoError(t, err)

		fooVersion := "foo"
		got, err := checkDaggerd(ctx, fooVersion)
		require.NoError(t, err)
		require.Equal(t, "docker-container://daggerd", got)
	})

	t.Run("update version", func(t *testing.T) {
		newVersion := "bar"
		got, err := checkDaggerd(ctx, newVersion)
		require.NoError(t, err)
		require.Equal(t, "docker-container://daggerd", got)
	})

	t.Run("stopped daggerd", func(t *testing.T) {
		err := StopDaggerd(ctx)
		require.NoError(t, err)

		newVersion := "bar"
		got, err := checkDaggerd(ctx, newVersion)
		require.NoError(t, err)
		require.Equal(t, "docker-container://daggerd", got)
	})
}
