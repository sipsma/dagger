package dagql

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A fetch's answer carries each entry asked as a root, with its record, and
// the entries it references as identity rows. Decoding, it carries the
// entry's child rows too, followed down, as records: here P's dependency Q,
// which P's call doesn't reference. An entry the Cloud doesn't have is
// missing.
func TestStoredRowsCarriesTheRowsAskedAndTheirChildren(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p, q := storedBundleTestChain(t)

	got, missing, err := cloud.StoredRows(ctx, []uint64{p, 12345}, time.Now().Unix(), false)
	require.NoError(t, err)
	require.Equal(t, []uint64{12345}, missing)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2)
	require.Empty(t, rows[p].Held, "P as its record")
	require.NotEmpty(t, rows[q].Held, "Q as an identity row")
	require.NotContains(t, rows, r, "nothing above P")
	require.Len(t, got.Bundle.Roots, 1)

	got, missing, err = cloud.StoredRows(ctx, []uint64{r}, time.Now().Unix(), true)
	require.NoError(t, err)
	require.Empty(t, missing)
	rows = storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 3)
	for _, number := range []uint64{r, p, q} {
		require.Empty(t, rows[number].Held, "entry %d travels as its record", number)
	}
	require.Len(t, got.Bundle.Roots, 1, "only the entry asked is a root")
}

// Decoding, an entry's call references stay identity rows: here R's call
// takes P as its receiver, so P isn't a child row of R's value.
func TestStoredRowsLeavesCallReferencesAsIdentityRows(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, p := heldTestChainedCloud(t)
	got, _, err := cloud.StoredRows(ctx, []uint64{r}, time.Now().Unix(), true)
	require.NoError(t, err)
	rows := storedBundleTestRows(got.Bundle)
	require.Len(t, rows, 2)
	require.Empty(t, rows[r].Held)
	require.NotEmpty(t, rows[p].Held, "R's receiver as an identity row")
}
