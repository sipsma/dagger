package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/stretchr/testify/require"
)

// boundTestRoots publishes n roots, each over two rows of its own, all
// three over a base the roots share: the first root adds four rows to a
// bundle, each later one three.
func boundTestRoots(t *testing.T, ctx context.Context, c *Cache, srv *Server, n int) []AnyResult {
	t.Helper()
	base := persistedListTestResult(t, ctx, c, srv, "bound-base", String("base"))
	var roots []AnyResult
	for i := range n {
		low := persistedListTestResult(t, ctx, c, srv, fmt.Sprintf("bound-low-%d", i), String(fmt.Sprintf("low %d", i)))
		mid := persistedListTestResult(t, ctx, c, srv, fmt.Sprintf("bound-mid-%d", i), String(fmt.Sprintf("mid %d", i)))
		root := persistedListTestResult(t, ctx, c, srv, fmt.Sprintf("bound-root-%d", i), String(fmt.Sprintf("root %d", i)))
		transferTestDependency(c, ctx, low, base)
		transferTestDependency(c, ctx, mid, low)
		transferTestDependency(c, ctx, root, mid)
		roots = append(roots, root)
	}
	return roots
}

// boundedCapture captures roots under a selection, and returns its bundle,
// the bundle's values' JSON as the bundle encodes them, and the roots it
// left out.
func boundedCapture(t *testing.T, ctx context.Context, c *Cache, selection ValueSelection) (ValueBundle, int64, []LeftOutRoot) {
	t.Helper()
	var bundle ValueBundle
	var left []LeftOutRoot
	err := c.WithExportedValues(ctx, selection, config.RefConfig{}, func(_ context.Context, exported *ExportedValues) error {
		bundle, left = exported.Bundle, exported.LeftOut
		return nil
	})
	require.NoError(t, err)
	values, err := json.Marshal(bundle.Values)
	require.NoError(t, err)
	roots, err := json.Marshal(bundle.Roots)
	require.NoError(t, err)
	return bundle, int64(len(values) + len(roots)), left
}

// A bounded capture takes the roots in order while the rows each adds fit,
// and leaves out the first that would pass the bound and every root after
// it, as ErrOverBound: under a bound of ten records, the first root's four
// rows and the second's three go, and the third, whose three would make
// eleven, waits with the fourth and fifth.
func TestBoundedExportStopsBeforeTheRootThatWouldPassItsRecords(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 5)
	bundle, _, left := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxRecords: 10})
	require.Len(t, bundle.Values, 10)
	require.Len(t, bundle.Roots, 3, "the first three roots")
	require.Len(t, left, 2)
	for i, root := range left {
		require.Equal(t, 3+i, root.Index, "in the selection's order")
		require.ErrorIs(t, root.Err, ErrOverBound)
	}
}

// Under a bound of bytes, a bounded capture's values never pass it, and
// what it keeps is the selection's roots in order up to a cut: for every
// bound from one root's values to all five roots', the bundle carries the
// first k roots, and its values and roots encode within the bound.
func TestBoundedExportNeverPassesItsBytes(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 5)
	_, whole, _ := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, WithoutOffers: true})
	for bound := whole / 5; bound <= 4*whole; bound += whole / 20 {
		bundle, size, left := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxBytes: bound, WithoutOffers: true})
		kept := len(bundle.Roots)
		require.Positive(t, kept)
		if kept > 1 {
			require.LessOrEqual(t, size, bound, "under a bound of %d bytes", bound)
		}
		require.Len(t, left, 5-kept)
		for i, root := range left {
			require.Equal(t, kept+i, root.Index)
			require.ErrorIs(t, root.Err, ErrOverBound)
		}
	}
	bundle, _, left := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxBytes: 4 * whole, WithoutOffers: true})
	require.Len(t, bundle.Roots, 5, "a bound over the whole bundle cuts nothing")
	require.Empty(t, left)
}

// The first root a bounded capture keeps goes, whatever it adds: under a
// bound of one record, the first root goes with its four rows, alone.
func TestBoundedExportSendsItsFirstRootOverTheBound(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 3)
	bundle, _, left := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxRecords: 1, MaxBytes: 1, WithoutOffers: true})
	require.Len(t, bundle.Roots, 1)
	require.Len(t, bundle.Values, 4)
	require.Len(t, left, 2)
}

// A busy root a bounded capture leaves out takes no place under its bound,
// and a root left out for the bound names no busy row.
func TestBoundedExportCountsOnlyTheRootsItKeeps(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 3)
	busy := roots[0].cacheSharedResult()
	busy.lazyMu.Lock()
	busy.lazyWhole.syncPending = true
	busy.lazyMu.Unlock()
	defer func() {
		busy.lazyMu.Lock()
		busy.lazyWhole.syncPending = false
		busy.lazyMu.Unlock()
	}()
	bundle, _, left := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxRecords: 7})
	require.Len(t, bundle.Roots, 2, "the second and third: four rows and three")
	require.Len(t, left, 1)
	require.Equal(t, 0, left[0].Index)
	require.False(t, errors.Is(left[0].Err, ErrOverBound))
}

// A bounded capture encodes each record once, to count it, and the bundle
// writes that encoding out: the same JSON as the bundle encoded afresh.
func TestBoundedExportEncodesTheSameJSON(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 3)
	bundle, _, _ := boundedCapture(t, ctx, c, ValueSelection{Roots: roots, MaxBytes: 1 << 30, WithoutOffers: true})
	encoded, err := json.Marshal(bundle)
	require.NoError(t, err)
	cached := 0
	for i := range bundle.Values {
		if bundle.Values[i].encodedRecord != nil {
			cached++
		}
		bundle.Values[i].encodedRecord = nil
	}
	require.Positive(t, cached, "the capture kept its records' encodings")
	afresh, err := json.Marshal(bundle)
	require.NoError(t, err)
	require.JSONEq(t, string(afresh), string(encoded))
	require.Equal(t, string(afresh), string(encoded))
}

// A bound of bytes counts records without their offers, which the bundle
// relocates after counting: a selection bounded by bytes that keeps them
// is refused.
func TestBoundedExportOfBytesLeavesOutOffers(t *testing.T) {
	t.Parallel()
	ctx, c, srv := transferTestCache(t)
	roots := boundTestRoots(t, ctx, c, srv, 2)
	err := c.WithExportedValues(ctx, ValueSelection{Roots: roots, MaxBytes: 1 << 20}, config.RefConfig{}, func(context.Context, *ExportedValues) error {
		t.Fatal("a bounded capture that keeps offers delivered")
		return nil
	})
	require.ErrorContains(t, err, "must leave out its records' offers")
}
