package dagql

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeShapeTestBroken returns a copy of bundle whose root record's object
// payload isn't JSON.
func mergeShapeTestBroken(t *testing.T, bundle ValueBundle) ValueBundle {
	t.Helper()
	broken, err := cloneValueBundle(bundle)
	require.NoError(t, err)
	require.Len(t, broken.Values, 1)
	require.Equal(t, persistedResultKindObject, broken.Values[0].Record.Envelope.Kind)
	broken.Values[0].Record.Envelope.ObjectJSON = json.RawMessage(`{"text":`)
	return broken
}

// A merge's shape check reads no payload: the payload of a record merge keeps
// on the entry that holds its value is never used, so a bundle whose kept
// record's payload isn't JSON merges, and lands on that entry. A record merge
// installs is still checked in full, and refused.
func TestMergeShapeReadsNoKeptPayload(t *testing.T) {
	bundle, _ := mergeTestSource(t, "root", "value")
	broken := mergeShapeTestBroken(t, bundle)

	ctx, c, _ := transferTestCache(t)
	first, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	again, err := c.MergeValues(ctx, cloudCacheID, broken)
	require.NoError(t, err, "a kept record's payload isn't read")
	require.True(t, again.Committed)
	require.Equal(t, first.Values[0].Number, again.Values[0].Number)

	ctx, c, _ = transferTestCache(t)
	_, err = c.MergeValues(ctx, cloudCacheID, broken)
	require.ErrorContains(t, err, "invalid object envelope", "an installed record's payload is checked in full")
}

// With _EXPERIMENTAL_DAGGER_MERGE_SHAPE_SKIP_JSON=0, the shape check reads
// every payload again, and refuses the kept record's.
func TestMergeShapeReadsPayloadsWhenSwitchedOff(t *testing.T) {
	t.Setenv(mergeShapeSkipJSONEnv, "0")
	bundle, _ := mergeTestSource(t, "root", "value")
	broken := mergeShapeTestBroken(t, bundle)
	ctx, c, _ := transferTestCache(t)
	_, err := c.MergeValues(ctx, cloudCacheID, bundle)
	require.NoError(t, err)
	_, err = c.MergeValues(ctx, cloudCacheID, broken)
	require.ErrorContains(t, err, "invalid object envelope")
}
