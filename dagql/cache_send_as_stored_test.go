package dagql

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// storedRecordsInTransferForm checks every stored record of a blob-backed
// cache against its normalized transfer form (normalizeTransferRecord): a
// record the Cloud stored from an engine's export is already in that form, so
// normalizing it changes nothing but its offers, which a transfer record
// never carries.
func storedRecordsInTransferForm(t *testing.T, cloud *Cache) int {
	t.Helper()
	cloud.egraphMu.RLock()
	defer cloud.egraphMu.RUnlock()
	checked := 0
	for id, res := range cloud.resultsByID {
		if res.persistedEnvelope == nil {
			continue
		}
		full, err := res.fullStoredEnvelope()
		require.NoError(t, err, "entry %d", id)
		stored := PersistedRecord{ResultID: uint64(id), Envelope: *full, Call: res.loadResultCall()}
		stored.Envelope.PendingOffers = nil
		normalized, err := normalizeTransferRecord(stored)
		require.NoError(t, err, "entry %d", id)
		normalized.Envelope.PendingOffers = nil
		want, err := json.Marshal(stored)
		require.NoError(t, err)
		got, err := json.Marshal(normalized)
		require.NoError(t, err)
		require.JSONEq(t, string(want), string(got), "entry %d normalizes to another form", id)
		checked++
	}
	return checked
}

// A record the Cloud stores from an engine's export is already in transfer
// form: normalizing it again changes nothing, so the Cloud can send its
// stored record as it is. Here a root over a dependency, with a part, and a
// call kept on a receiver.
func TestStoredRecordsAreInTransferForm(t *testing.T) {
	t.Parallel()
	ctx, cloud, r, _ := storedBundleTestCloud(t, "root")
	stored, err := cloud.SetStoredPart(ctx, r, testLiveOffer(), 0)
	require.NoError(t, err)
	require.True(t, stored)
	require.Equal(t, 2, storedRecordsInTransferForm(t, cloud))

	test := newReceiverCallTest(t)
	require.Equal(t, 2, storedRecordsInTransferForm(t, test.cloud))
}
