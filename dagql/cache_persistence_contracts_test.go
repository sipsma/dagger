package dagql

import (
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestDerivePersistResultKeyFromID(t *testing.T) {
	id := cacheTestID("persist-result-key")
	key, err := derivePersistResultKey(id)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(key), id.Digest().String()))
}

func TestSharedResultPersistResultKey(t *testing.T) {
	id := cacheTestID("shared-result-original-request-id")
	res := &sharedResult{
		originalRequestID: id,
	}
	key, err := res.persistResultKey()
	assert.NilError(t, err)
	assert.Check(t, is.Equal(string(key), id.Digest().String()))
}

func TestPersistEqFactRowCanonicalizesDigestOrder(t *testing.T) {
	row, err := newPersistEqFactRow(
		cachePersistResultKey("owner-key"),
		"zzzz",
		"aaaa",
	)
	assert.NilError(t, err)
	assert.Check(t, is.Equal(row.LHSDigest, "aaaa"))
	assert.Check(t, is.Equal(row.RHSDigest, "zzzz"))
}

func TestPersistEqFactRowRejectsEmptyOwner(t *testing.T) {
	_, err := newPersistEqFactRow("", "a", "b")
	assert.Assert(t, err != nil)
}
