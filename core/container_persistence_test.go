package core

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/stretchr/testify/require"
)

func TestContainerEncodePersistedObjectRetainedCompletedLazyUsesReadyForm(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Container]{}))
	parent := containerPersistenceTestResult(t, srv, "container-parent", NewContainer(Platform{
		OS:           "linux",
		Architecture: "amd64",
	}))

	completedState := NewLazyState()
	completedState.LazyInitComplete = true
	ctr := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	ctr.Lazy = &ContainerWithEntrypointLazy{
		LazyState: completedState,
		Parent:    parent,
		Args:      []string{"sh"},
	}

	enc, err := ctr.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedContainerPayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedContainerFormReady, payload.Form)
	require.Empty(t, payload.LazyJSON)
}

func TestContainerEncodePersistedObjectPendingLazyUsesLazyForm(t *testing.T) {
	t.Parallel()

	srv := newCoreDagqlServerForTest(t, &Query{})
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Container]{}))
	parent := containerPersistenceTestResult(t, srv, "container-parent", NewContainer(Platform{
		OS:           "linux",
		Architecture: "amd64",
	}))

	ctr := NewContainer(Platform{OS: "linux", Architecture: "amd64"})
	ctr.Lazy = &ContainerWithEntrypointLazy{
		LazyState: NewLazyState(),
		Parent:    parent,
		Args:      []string{"sh"},
	}

	enc, err := ctr.EncodePersistedObject(context.Background(), &cacheVolumeTestPersistedObjectCache{resultID: 17})
	require.NoError(t, err)

	var payload persistedContainerPayload
	require.NoError(t, json.Unmarshal(enc.JSON, &payload))
	require.Equal(t, persistedContainerFormLazy, payload.Form)
	require.NotEmpty(t, payload.LazyJSON)
}

func containerPersistenceTestResult(t *testing.T, srv *dagql.Server, op string, ctr *Container) dagql.ObjectResult[*Container] {
	t.Helper()

	res, err := dagql.NewObjectResultForCall(ctr, srv, &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: op,
		Type:        dagql.NewResultCallType((&Container{}).Type()),
	})
	require.NoError(t, err)
	return res
}
