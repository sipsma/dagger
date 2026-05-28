package schema

import (
	"path/filepath"
	"testing"

	"github.com/dagger/dagger/core"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceLockExcludePattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ws := &core.Workspace{
		Path:     filepath.Join("apps", "api"),
		ClientID: "client-a",
	}
	ws.SetHostPath(root)

	t.Run("exact workspace path", func(t *testing.T) {
		t.Parallel()

		workspacePath := filepath.Join(root, "apps", "api")
		pattern, ok := workspaceLockExcludePattern(
			ws,
			"client-a",
			workspacePath,
			workspacePath,
		)
		require.True(t, ok)
		require.Equal(t, filepath.Join(".dagger", "lock"), pattern)
	})

	t.Run("rebased snapshot root", func(t *testing.T) {
		t.Parallel()

		pattern, ok := workspaceLockExcludePattern(
			ws,
			"client-a",
			root,
			filepath.Join(root, "apps", "api"),
		)
		require.True(t, ok)
		require.Equal(t, filepath.Join("apps", "api", ".dagger", "lock"), pattern)
	})

	t.Run("ancestor snapshot", func(t *testing.T) {
		t.Parallel()

		pattern, ok := workspaceLockExcludePattern(
			ws,
			"client-a",
			root,
			root,
		)
		require.True(t, ok)
		require.Equal(t, filepath.Join("apps", "api", ".dagger", "lock"), pattern)
	})

	t.Run("descendant path", func(t *testing.T) {
		t.Parallel()

		lockDir := filepath.Join(root, "apps", "api", ".dagger")
		_, ok := workspaceLockExcludePattern(
			ws,
			"client-a",
			lockDir,
			lockDir,
		)
		require.False(t, ok)
	})

	t.Run("different path", func(t *testing.T) {
		t.Parallel()

		siblingPath := filepath.Join(root, "apps", "web")
		_, ok := workspaceLockExcludePattern(
			ws,
			"client-a",
			siblingPath,
			siblingPath,
		)
		require.False(t, ok)
	})

	t.Run("different client", func(t *testing.T) {
		t.Parallel()

		_, ok := workspaceLockExcludePattern(
			ws,
			"client-b",
			filepath.Join(root, "apps", "api"),
			filepath.Join(root, "apps", "api"),
		)
		require.False(t, ok)
	})

	t.Run("remote workspace", func(t *testing.T) {
		t.Parallel()

		remote := &core.Workspace{
			Path:     ".",
			ClientID: "client-a",
		}
		_, ok := workspaceLockExcludePattern(remote, "client-a", root, root)
		require.False(t, ok)
	})
}
