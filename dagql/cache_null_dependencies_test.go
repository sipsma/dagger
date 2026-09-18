package dagql

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// A call that resolves to nothing (a nullable field answered with null, a
// module function returning Void) is published as a null row whose frame
// still names its receiver, its module and its arguments. Those rows are
// the null row's dependencies like any other result's: they keep the
// referenced rows alive while the null row is retained, and an export of
// the null row carries them.
func TestNullResultRecordsFrameDependencies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	ctx, cache, srv := persistedListTestCache(t, path)
	receiver := persistedListTestResult(t, ctx, cache, srv, "receiver-int", Int(7))
	module := persistedListTestResult(t, ctx, cache, srv, "module-int", Int(9))
	argument := persistedListTestResult(t, ctx, cache, srv, "argument-int", Int(11))
	receiverID, err := cache.PersistedResultID(receiver)
	require.NoError(t, err)
	moduleID, err := cache.PersistedResultID(module)
	require.NoError(t, err)
	argumentID, err := cache.PersistedResultID(argument)
	require.NoError(t, err)

	frame := &ResultCall{
		Kind:     ResultCallKindField,
		Field:    "nothing",
		Type:     NewResultCallType(&ast.Type{NamedType: "Int"}),
		Receiver: &ResultCallRef{ResultID: receiverID},
		Module:   &ResultCallModule{Name: "demo", ResultRef: &ResultCallRef{ResultID: moduleID}},
		Args:     []*ResultCallArg{{Name: "in", Value: &ResultCallLiteral{Kind: ResultCallLiteralKindResultRef, ResultRef: &ResultCallRef{ResultID: argumentID}}}},
	}
	res, err := cache.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		return nil, nil
	})
	require.NoError(t, err)
	require.Nil(t, res.Unwrap(), "a null answer carries no value")
	nullID, err := cache.PersistedResultID(res)
	require.NoError(t, err)

	entries, err := cache.SessionResults(ctx, "test-session")
	require.NoError(t, err)
	var null *SessionResultEntry
	for i := range entries {
		if entries[i].ResultID == nullID {
			null = &entries[i]
		}
	}
	require.NotNil(t, null, "the null answer has a row")
	require.Equal(t, "nothing", null.Field)
	require.ElementsMatch(t, []uint64{receiverID, moduleID, argumentID}, null.DependsOn, "the null row depends on every row its frame names")

	require.NoError(t, cache.WithResultsByNumber(ctx, []uint64{null.ResultID}, func(ctx context.Context, found []AnyResult, missing []uint64) error {
		require.Empty(t, missing)
		require.NotNil(t, found[0])
		return cache.WithExportedValues(ctx, ValueSelection{Roots: []AnyResult{found[0]}}, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
			require.Len(t, values.Bundle.Values, 4, "the null row and the three rows it names")
			return nil
		})
	}))
}
