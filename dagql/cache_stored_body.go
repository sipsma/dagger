package dagql

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// A blob-backed cache (the Cloud) never decodes a stored value: it sends it,
// saves it and answers questions about its parts. So it keeps each stored
// envelope as compressed JSON (storedBody), and in persistedEnvelope only the
// envelope's small fields, which tell a stored value by its presence and its
// kind. Readers of the whole envelope, a bundle, a size measure and a save,
// decompress it (fullStoredEnvelope). The record's declared parts that
// questions about absence and borrowing read are worked out once when the
// value is stored (declaredParts), so those questions never decompress.

var (
	storedBodyCodecOnce sync.Once
	storedBodyEncoder   *zstd.Encoder
	storedBodyDecoder   *zstd.Decoder
)

func storedBodyCodec() (*zstd.Encoder, *zstd.Decoder) {
	storedBodyCodecOnce.Do(func() {
		var err error
		storedBodyEncoder, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
		if err != nil {
			panic(fmt.Sprintf("stored body encoder: %v", err))
		}
		storedBodyDecoder, err = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0))
		if err != nil {
			panic(fmt.Sprintf("stored body decoder: %v", err))
		}
	})
	return storedBodyEncoder, storedBodyDecoder
}

// declaredPart is one part a stored record declares that a question about
// absence or borrowing reads: its address key and its state, "absent" or
// "pending".
type declaredPart struct {
	key   string
	state string
}

// lightenStoredValueLocked compresses res's stored envelope into storedBody,
// works out its declared parts, and leaves only the envelope's small fields
// in persistedEnvelope. It does nothing on an engine's cache, for an entry
// with no stored envelope, or for one already light. Requires egraphMu for
// writing, or an entry no reader can reach yet.
func (c *Cache) lightenStoredValueLocked(res *sharedResult) error {
	if !c.blobBacked || res == nil || res.persistedEnvelope == nil || res.isLightLocked() {
		return nil
	}
	env := res.persistedEnvelope
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("lighten stored value %d: %w", res.id, err)
	}
	enc, _ := storedBodyCodec()
	body := enc.EncodeAll(raw, make([]byte, 0, len(raw)/3))
	parts, perr := declaredPartsOf(res, env)
	light := &PersistedResultEnvelope{
		Imported:              env.Imported,
		PendingOffers:         env.PendingOffers,
		PersistDeclined:       env.PersistDeclined,
		Version:               env.Version,
		Kind:                  env.Kind,
		TypeName:              env.TypeName,
		ObjectCodec:           env.ObjectCodec,
		ResultID:              env.ResultID,
		SessionResourceHandle: env.SessionResourceHandle,
	}
	res.payloadMu.Lock()
	res.storedBody = body
	res.lightEnvelope = light
	res.declaredParts = parts
	res.declaredPartsErr = perr
	res.persistedEnvelope = light
	res.payloadMu.Unlock()
	return nil
}

// declaredPartsOf maps env's declared outputs, as the transfer codec reads
// them, to the parts a question about absence or borrowing reads.
func declaredPartsOf(res *sharedResult, env *PersistedResultEnvelope) ([]declaredPart, error) {
	outputs, err := mapTransferredOutputs(PersistedRecord{ResultID: uint64(res.id), Envelope: *env, Call: res.loadResultCall()})
	if err != nil {
		return nil, err
	}
	var parts []declaredPart
	for _, output := range outputs {
		if output.State != "absent" && output.State != "pending" {
			continue
		}
		key, err := partAddressKey(output.Address)
		if err != nil {
			continue
		}
		parts = append(parts, declaredPart{key: key, state: output.State})
	}
	return parts, nil
}

// fullStoredEnvelope returns res's whole stored envelope: the one in
// persistedEnvelope, or, for a light entry, its body decompressed and
// decoded, a new value each time. Requires egraphMu, or the light envelope
// and body read together under it (decodeStoredBody).
func (res *sharedResult) fullStoredEnvelope() (*PersistedResultEnvelope, error) {
	return decodeStoredBody(res.persistedEnvelope, res.lightEnvelope, res.storedBody)
}

// isLightLocked reports whether res's envelope is the light one its
// lightening left: an envelope replaced since is whole.
func (res *sharedResult) isLightLocked() bool {
	return res.storedBody != nil && res.persistedEnvelope != nil && res.persistedEnvelope == res.lightEnvelope
}

// decodeStoredBody returns the whole envelope of env: its body decompressed
// when env is the light envelope the body goes with, env itself otherwise.
func decodeStoredBody(env, light *PersistedResultEnvelope, body []byte) (*PersistedResultEnvelope, error) {
	if env == nil || body == nil || env != light {
		return env, nil
	}
	_, dec := storedBodyCodec()
	raw, err := dec.DecodeAll(body, nil)
	if err != nil {
		return nil, fmt.Errorf("decompress stored envelope: %w", err)
	}
	full := new(PersistedResultEnvelope)
	if err := json.Unmarshal(raw, full); err != nil {
		return nil, fmt.Errorf("decode stored envelope: %w", err)
	}
	return full, nil
}

// declaredPartsLocked returns res's declared parts: kept for a light entry,
// mapped from its envelope otherwise. Requires egraphMu.
func (res *sharedResult) declaredPartsLocked() ([]declaredPart, error) {
	if res.isLightLocked() {
		return res.declaredParts, res.declaredPartsErr
	}
	if res.persistedEnvelope == nil {
		return nil, nil
	}
	return declaredPartsOf(res, res.persistedEnvelope)
}

// lightenStoredValuesLocked lightens every stored entry of a blob-backed
// cache, as a restore leaves them. Requires egraphMu for writing.
func (c *Cache) lightenStoredValuesLocked() error {
	if !c.blobBacked {
		return nil
	}
	for _, res := range c.resultsByID {
		if err := c.lightenStoredValueLocked(res); err != nil {
			return err
		}
	}
	return nil
}
