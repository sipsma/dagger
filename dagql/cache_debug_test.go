package dagql

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestDebugCacheSnapshotIncludesResultMetadata(t *testing.T) {
	base, err := NewCache(t.Context(), "", nil, nil)
	assert.NilError(t, err)
	c := base

	attached, err := c.AttachResult(t.Context(), "test-session", noopTypeResolver{}, cacheTestDetachedResult(cacheTestIntCall("debugCache"), NewInt(123)))
	assert.NilError(t, err)
	shared := attached.cacheSharedResult()
	assert.Assert(t, shared != nil)

	var out bytes.Buffer
	err = c.WriteDebugCacheSnapshot(&out)
	assert.NilError(t, err)

	var snapshot CacheDebugSnapshot
	err = json.Unmarshal(out.Bytes(), &snapshot)
	assert.NilError(t, err)
	assert.Assert(t, is.Len(snapshot.Results, 1))
	assert.Assert(t, is.Len(snapshot.ResultDigestIndexes, 1))

	result := snapshot.Results[0]
	assert.Equal(t, result.SharedResultID, uint64(shared.id))
	assert.Assert(t, result.ResultCall != nil)
	assert.Equal(t, result.ResultCall.Field, "debugCache")
	assert.Assert(t, result.ResultCallRecipeDigest != "")
	assert.Assert(t, result.ResultCallRecipeDigestError == "")
	assert.Assert(t, slices.Contains(result.IndexedDigests, result.ResultCallRecipeDigest))
	assert.Assert(t, len(result.AssociatedTermIDs) > 0)

	index := snapshot.ResultDigestIndexes[0]
	assert.Equal(t, index.Digest, result.ResultCallRecipeDigest)
	assert.DeepEqual(t, index.SharedResultIDs, []uint64{uint64(shared.id)})
}

func TestDebugCacheSnapshotIncludesCompletedArbitraryCalls(t *testing.T) {
	base, err := NewCache(t.Context(), "", nil, nil)
	assert.NilError(t, err)
	c := base

	_, err = c.GetOrInitArbitrary(t.Context(), "debug-session", "debug-arbitrary", ArbitraryValueFunc("hello"))
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, c.ReleaseSession(t.Context(), "debug-session"))
	}()

	var out bytes.Buffer
	err = c.WriteDebugCacheSnapshot(&out)
	assert.NilError(t, err)

	var snapshot CacheDebugSnapshot
	err = json.Unmarshal(out.Bytes(), &snapshot)
	assert.NilError(t, err)
	assert.Assert(t, is.Len(snapshot.CompletedArbitraryCalls, 1))

	call := snapshot.CompletedArbitraryCalls[0]
	assert.Equal(t, call.CallKey, "debug-arbitrary")
	assert.Assert(t, call.Completed)
	assert.Assert(t, call.HasValue)
	assert.Equal(t, call.ValueType, "string")
	assert.Equal(t, call.OwnerSessionCount, 1)
}

func TestDebugCachemoneyStatsReturnsClone(t *testing.T) {
	base, err := NewCache(t.Context(), "", nil, nil)
	assert.NilError(t, err)
	c := base

	c.recordCachemoneyMaterialization("rootfs", CachemoneyMaterializationHydrated)
	c.recordCachemoneyRecompute(CachemoneyRecomputeReasonIndexMiss)
	c.recordCachemoneyHydrationFailure(CachemoneyRecomputeReasonFetchFailed)
	c.recordCachemoneyBlobDownloaded(42)
	c.recordCachemoneyBlobUpload(true)
	c.recordCachemoneyViability("remote_snapshot_blobs_available", true, true)

	stats := c.DebugCachemoneyStats()
	stats.MaterializationOutcomesByRole["rootfs"][CachemoneyMaterializationHydrated] = 99
	stats.RecomputeReasons[CachemoneyRecomputeReasonIndexMiss] = 99
	stats.HydrationFailures[CachemoneyRecomputeReasonFetchFailed] = 99
	stats.ViabilityCensusByReason["remote_snapshot_blobs_available"] = CachemoneyViabilityCount{}

	again := c.DebugCachemoneyStats()
	assert.Equal(t, again.MaterializationOutcomesByRole["rootfs"][CachemoneyMaterializationHydrated], uint64(1))
	assert.Equal(t, again.RecomputeReasons[CachemoneyRecomputeReasonIndexMiss], uint64(1))
	assert.Equal(t, again.HydrationFailures[CachemoneyRecomputeReasonFetchFailed], uint64(1))
	assert.Equal(t, again.BytesDownloaded, uint64(42))
	assert.Equal(t, again.BlobsDownloaded, uint64(1))
	assert.Equal(t, again.BlobsUploadSkippedAlreadyExists, uint64(1))
	assert.DeepEqual(t, again.ViabilityCensusByReason["remote_snapshot_blobs_available"], CachemoneyViabilityCount{
		Total:    1,
		Viable:   1,
		Eligible: 1,
	})
}
