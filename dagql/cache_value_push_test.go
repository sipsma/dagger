package dagql

import (
	"context"
	"testing"

	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/stretchr/testify/require"
)

// pushTestCapture captures roots for a push with rows, as the integration's
// pusher does, and returns the bundle and the rows it carried.
func pushTestCapture(t *testing.T, ctx context.Context, c *Cache, known *KnownRows, rows *PushRows, roots ...AnyResult) (ValueBundle, ExportedRows) {
	t.Helper()
	var bundle ValueBundle
	var carried ExportedRows
	require.NoError(t, c.WithExportedValues(ctx, ValueSelection{Roots: roots, Known: known, Push: rows}, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
		bundle, carried = values.Bundle, values.Rows
		return nil
	}))
	return bundle, carried
}

// pushTestFull counts a bundle's rows sent in full and held.
func pushTestFull(bundle ValueBundle) (full, held int) {
	for _, row := range bundle.Values {
		if row.Held != "" {
			held++
		} else {
			full++
		}
	}
	return full, held
}

// pushTestLacks are the rows the recipient says it lacks, as a set.
func pushTestLacks(recipient *Cache, bundle ValueBundle) map[uint64]bool {
	full := map[uint64]bool{}
	for _, number := range recipient.LackedRows(bundle.Values) {
		full[number] = true
	}
	return full
}

// A push asks first about every row of its roots' closures the Cloud isn't
// known to have, naming each as a held row; the Cloud says which it lacks.
// The push then sends those in full and the others held, and the Cloud
// stores the records it lacked, learning the engine's holding of every row
// the push carried. A second engine that pushes the same results sends no
// record at all: the Cloud lacks none.
func TestPushSendsTheCloudOnlyTheRecordsItLacks(t *testing.T) {
	t.Parallel()
	build := func(t *testing.T) (context.Context, *Cache, AnyResult, AnyResult) {
		ctx, c, srv := transferTestCache(t)
		child := persistedListTestResult(t, ctx, c, srv, "push-child", String("child"))
		root := persistedListTestResult(t, ctx, c, srv, "push-root", String("root"))
		transferTestDependency(c, ctx, root, child)
		return ctx, c, child, root
	}
	cctx, cloud := storedPartTestCache(t, WithBlobStore())

	// Another engine stored the child already.
	octx, other, otherChild, _ := build(t)
	reply, err := cloud.MergeValues(cctx, "other", exportTestBundle(t, octx, other, otherChild))
	require.NoError(t, err)
	require.True(t, reply.Committed)

	ctx, engine, child, root := build(t)
	known := NewKnownRows()
	listed, _ := pushTestCapture(t, ctx, engine, known, &PushRows{List: true}, root)
	full, held := pushTestFull(listed)
	require.Equal(t, 0, full, "the list carries no record")
	require.Equal(t, 2, held, "it names the root and the row below it")
	lacks := pushTestLacks(cloud, listed)
	require.Equal(t, map[uint64]bool{uint64(root.cacheSharedResult().id): true}, lacks, "the Cloud lacks only the root")

	push, rows := pushTestCapture(t, ctx, engine, known, &PushRows{Full: lacks}, root)
	full, held = pushTestFull(push)
	require.Equal(t, 1, full, "only the root goes in full")
	require.Equal(t, 1, held)
	reply, err = cloud.MergeValues(cctx, "engine", push)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Equal(t, MergeRowCounts{Held: 1, Created: 1}, reply.Rows)
	known.Merged(rows)
	require.Equal(t, 2, known.Len(), "both rows are known once the push merged")
	cloud.egraphMu.RLock()
	for _, res := range []AnyResult{child, root} {
		_, h := cloud.holdingLocked(HolderKey{Cache: "engine", Number: uint64(res.cacheSharedResult().id)})
		require.NotNil(t, h, "the Cloud holds the engine's holding of each row")
	}
	cloud.egraphMu.RUnlock()

	// A third engine computed both: the Cloud lacks nothing.
	tctx, third, _, thirdRoot := build(t)
	listed, _ = pushTestCapture(t, tctx, third, NewKnownRows(), &PushRows{List: true}, thirdRoot)
	lacks = pushTestLacks(cloud, listed)
	require.Empty(t, lacks)
	push, _ = pushTestCapture(t, tctx, third, NewKnownRows(), &PushRows{Full: lacks}, thirdRoot)
	full, _ = pushTestFull(push)
	require.Zero(t, full, "no record is sent twice")
	reply, err = cloud.MergeValues(cctx, "third", push)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	require.Equal(t, MergeRowCounts{Held: 1}, reply.Rows)

	// What the Cloud is known to have goes held, with nothing below it.
	again, _ := pushTestCapture(t, ctx, engine, known, &PushRows{List: true}, root)
	full, held = pushTestFull(again)
	require.Equal(t, 0, full)
	require.Equal(t, 1, held, "a known root stands for its closure")
}

// A row the Cloud says it lacks goes in full, though the engine knew it as
// merged: the Cloud may have dropped it since.
func TestPushSendsInFullAKnownRowTheCloudLacks(t *testing.T) {
	t.Parallel()
	ctx, engine, srv := transferTestCache(t)
	child := persistedListTestResult(t, ctx, engine, srv, "known-child", String("child"))
	root := persistedListTestResult(t, ctx, engine, srv, "known-root", String("root"))
	transferTestDependency(engine, ctx, root, child)
	known := NewKnownRows()
	_, rows := pushTestCapture(t, ctx, engine, known, &PushRows{Full: map[uint64]bool{uint64(child.cacheSharedResult().id): true}}, child)
	known.Merged(rows)
	require.Equal(t, 1, known.Len(), "the engine knows the child as merged")

	push, _ := pushTestCapture(t, ctx, engine, known, &PushRows{Full: map[uint64]bool{uint64(root.cacheSharedResult().id): true, uint64(child.cacheSharedResult().id): true}}, root)
	full, held := pushTestFull(push)
	require.Equal(t, 2, full, "the known child goes in full, as the Cloud asked")
	require.Zero(t, held)
}
