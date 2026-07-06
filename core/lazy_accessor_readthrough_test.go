package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	"github.com/stretchr/testify/require"
)

// readthroughTestLazy fills the directory's Dir accessor with a fixed value
// when evaluated.
type readthroughTestLazy struct {
	LazyState
	dirValue string
}

func (l *readthroughTestLazy) Evaluate(ctx context.Context, dir *Directory) error {
	return l.LazyState.Evaluate(ctx, "Directory", func(ctx context.Context) error {
		dir.Dir.SetValue(l.dirValue)
		return nil
	})
}

func (l *readthroughTestLazy) AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error) {
	return nil, nil
}

func (l *readthroughTestLazy) EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error) {
	return nil, fmt.Errorf("readthrough test lazy is not persistable")
}

// TestLazyAccessorCloneCopiedBeforeRealizeReadsThrough pins the
// clone-copied-before-realize scenario: a value struct's accessor is copied
// (empty) before the canonical value realizes, then read via GetOrEval with
// the canonical result. The canonical value holds the content; the copy must
// resolve to it instead of erroring.
func TestLazyAccessorCloneCopiedBeforeRealizeReadsThrough(t *testing.T) {
	t.Parallel()

	ctx := engine.ContextWithClientMetadata(t.Context(), &engine.ClientMetadata{
		ClientID:  "core-test-client",
		SessionID: "test-session",
	})

	query := &Query{Server: &mockServer{}}
	srv := newCoreDagqlServerForTest(t, query)
	srv.InstallObject(dagql.NewClass(srv, dagql.ClassOpts[*Directory]{}))

	dagCache, err := dagql.NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, dagCache.Close(context.Background()))
	})
	ctx = dagql.ContextWithCache(ctx, dagCache)

	orig := &Directory{
		Lazy:     &readthroughTestLazy{LazyState: NewLazyState(), dirValue: "canonical-dir"},
		Dir:      newDirectoryDirAccessor(),
		Snapshot: newDirectorySnapshotAccessor(),
	}

	dirCall := &dagql.ResultCall{
		Kind:        dagql.ResultCallKindSynthetic,
		SyntheticOp: "readthrough-test-directory",
		Type:        dagql.NewResultCallType((&Directory{}).Type()),
	}
	resAny, err := dagCache.GetOrInitCall(ctx, "test-session", srv, &dagql.CallRequest{
		ResultCall: dirCall,
	}, func(context.Context) (dagql.AnyResult, error) {
		return dagql.NewObjectResultForCall(orig, srv, dirCall)
	})
	require.NoError(t, err)
	source, ok := resAny.(dagql.ObjectResult[*Directory])
	require.True(t, ok, "expected ObjectResult[*Directory], got %T", resAny)

	// Copy the accessor the way clone helpers do — peek, and copy the value
	// only if present. The canonical value has not realized yet, so the copy
	// stays empty.
	clone := &Directory{
		Dir: newDirectoryDirAccessor(),
	}
	if v, ok := orig.Dir.Peek(); ok {
		clone.Dir.SetValue(v)
	}

	// The canonical value realizes: its own accessor fills.
	got, err := source.Self().Dir.GetOrEval(ctx, source.Result)
	require.NoError(t, err)
	require.Equal(t, "canonical-dir", got)

	// The copy taken before realization reads via GetOrEval against the same
	// result. The content exists on the canonical value.
	cloneGot, err := clone.Dir.GetOrEval(ctx, source.Result)
	require.NoError(t, err)
	require.Equal(t, "canonical-dir", cloneGot)
}
