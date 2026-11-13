package dagql

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dagger/dagger/dagql/call"
	"github.com/stretchr/testify/require"
)

// storedBundleTestChain merges an export of R, which depends on P, which
// depends on Q, into a new Cloud cache, and returns the Cloud's numbers for
// R, P and Q.
func storedBundleTestChain(t *testing.T) (context.Context, *Cache, uint64, uint64, uint64) {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	r := persistedListTestResult(t, actx, a, asrv, "chain-root", &transferTestValue{Text: "root"})
	p := persistedListTestResult(t, actx, a, asrv, "chain-mid", &transferTestValue{Text: "mid"})
	q := persistedListTestResult(t, actx, a, asrv, "chain-leaf", &transferTestValue{Text: "leaf"})
	transferTestDependency(a, actx, r, p)
	transferTestDependency(a, actx, p, q)
	bundle := exportTestBundle(t, actx, a, r)
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	reply, err := cloud.MergeValues(ctx, "cache-a", bundle)
	require.NoError(t, err)
	numbers := map[uint64]uint64{}
	for _, value := range reply.Values {
		for _, sent := range bundle.Values {
			if sent.Ordinal == value.Ordinal {
				numbers[sent.SenderNumber] = value.Number
			}
		}
	}
	rn, pn, qn := numbers[uint64(r.cacheSharedResult().id)], numbers[uint64(p.cacheSharedResult().id)], numbers[uint64(q.cacheSharedResult().id)]
	require.NotZero(t, rn)
	require.NotZero(t, pn)
	require.NotZero(t, qn)
	return ctx, cloud, rn, pn, qn
}

// storedBundleTestRows returns a bundle's rows by their Cloud number.
func storedBundleTestRows(bundle ValueBundle) map[uint64]TransferredValue {
	rows := map[uint64]TransferredValue{}
	for _, value := range bundle.Values {
		rows[value.SenderNumber] = value
	}
	return rows
}

// A stored bundle carries its roots' own records and, for each entry they
// reference, only an identity row: a held row naming the entry by its recipe
// and its extra digests, with no record, no dependencies and nothing below it
// walked. A root that another root references still travels as its record.
// StoredSizes prices the same rows.
func TestStoredBundleCarriesRootsAndIdentityRows(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, q := storedBundleTestChain(t)
	content := testDigest("chain-mid-content")
	require.NoError(t, cloud.TeachContentDigest(ctx, Result[Typed]{shared: storedBundleTestEntry(cloud, p)}, content))

	got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2, "R's record and P's identity row, and nothing of Q")
	require.Empty(t, rows[r].Held)
	require.NotNil(t, rows[r].Record.Call, "R travels as its record")
	require.NotEmpty(t, rows[p].Held, "P travels as an identity row")
	require.Nil(t, rows[p].Record.Call)
	require.Empty(t, rows[p].DependencyIDs)
	require.True(t, slices.Contains(rows[p].Extra, call.ExtraDigest{Digest: content, Label: call.ExtraDigestLabelContent}),
		"P's identity row names its content digest")
	require.Equal(t, []uint64{p}, got.Held)
	require.Len(t, got.Bundle.Roots, 1)

	sizes, err := cloud.StoredSizes(ctx, []uint64{r}, time.Now().Unix())
	require.NoError(t, err)
	storedSizesTestClose(t, storedSizesTestMeasure(t, got.Bundle), sizes)
	require.Equal(t, 2, sizes[0].Records)

	got, err = cloud.StoredBundle(ctx, []uint64{r, p}, time.Now().Unix())
	require.NoError(t, err)
	rows = storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 3)
	require.Empty(t, rows[r].Held)
	require.Empty(t, rows[p].Held, "P is a root, so it travels as its record though R reaches it first")
	require.NotEmpty(t, rows[q].Held, "Q, below P, is an identity row")
	require.Equal(t, []uint64{q}, got.Held)

	sizes, err = cloud.StoredSizes(ctx, []uint64{r, p}, time.Now().Unix())
	require.NoError(t, err)
	require.Equal(t, 3, sizes[0].Records+sizes[1].Records)
}

// storedTestEveryEntry returns the numbers of every entry of a Cloud with a
// stored record, in order: a bundle with them all as roots carries every
// record, as a bundle of the first carried its closure before identity rows.
func storedTestEveryEntry(c *Cache) []uint64 {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	var numbers []uint64
	for id, res := range c.resultsByID {
		if res.persistedEnvelope != nil && !res.noValueLocked() {
			numbers = append(numbers, uint64(id))
		}
	}
	slices.Sort(numbers)
	return numbers
}
