package dagql

import (
	"fmt"
	"testing"
)

// lanewaitFactsLocked is HoldingFacts' answer and the entry's facts for key,
// as a write would take them inside its own hold. Requires egraphMu.
func (c *Cache) lanewaitFactsLocked(key HolderKey, parts bool) (HoldingFacts, uint64, bool, int64, int, bool) {
	entry, h := c.holdingLocked(key)
	if h == nil {
		return HoldingFacts{}, 0, false, 0, 0, false
	}
	facts := HoldingFacts{
		TypeName:          handleString(h.typeName),
		ExpiresAtUnix:     h.expiresAtUnix,
		Retained:          h.retention.retained,
		Replacements:      h.replacements,
		Executed:          h.executed,
		FollowsFromInputs: h.follows,
		CallRow:           h.rowed,
		Deps:              h.deps,
		UnknownDeps:       h.unknownDeps,
	}
	if h.retention.retained {
		facts.RetentionExpiresAtUnix = h.retention.expiresAtUnix
	}
	if parts {
		facts.Parts, _ = heldPartAddresses(h.parts, false)
	}
	number, stored, expires, recipes := entryFactsLocked(entry)
	return facts, number, stored, expires, len(recipes), true
}

// BenchmarkLanewaitFacts prices, on a Cloud cache holding 30 earlier engines'
// rows and a fresh engine's 20,000 (benchmarkRerunRow), what a pass reads of
// each of the fresh engine's holdings: through RemoteHoldingInfos as a pass
// reads them now, uncontended; and taken inside a write's hold, with the parts
// decoded as HoldingFacts gives them and without; beside one more row's write,
// a new holding attached with its session hold, on the same cache.
func BenchmarkLanewaitFacts(b *testing.B) {
	const rows = 20_000
	ctx := b.Context()
	cloud, err := NewCache(ctx, "", nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cloud.CloseDiscardingPersistence() })
	for e := range 31 {
		engine := CacheID(fmt.Sprintf("earlier-%02d", e))
		if e == 30 {
			engine = "fresh"
		}
		for i := range rows {
			key := HolderKey{Cache: engine, Number: uint64(i + 1)}
			if _, err := cloud.AttachRemoteHolding(ctx, key, benchmarkRerunRow(i)); err != nil {
				b.Fatal(err)
			}
			if _, err := cloud.AddRemoteHold(ctx, key, fmt.Sprintf("s%d", i/49)); err != nil {
				b.Fatal(err)
			}
		}
	}
	keys := make([]HolderKey, rows)
	for i := range keys {
		keys[i] = HolderKey{Cache: "fresh", Number: uint64(i + 1)}
	}
	b.Run("read-now", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			held, ok := cloud.RemoteHoldingInfos(keys)
			if len(held) != rows || !ok[0] {
				b.Fatal("short read")
			}
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*rows), "ns/holding")
	})
	for _, parts := range []bool{true, false} {
		b.Run(fmt.Sprintf("in-hold/parts=%t", parts), func(b *testing.B) {
			b.ReportAllocs()
			cloud.egraphMu.Lock()
			defer cloud.egraphMu.Unlock()
			for b.Loop() {
				for _, key := range keys {
					if _, _, _, _, _, ok := cloud.lanewaitFactsLocked(key, parts); !ok {
						b.Fatal("no holding")
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*rows), "ns/holding")
		})
	}
	b.Run("row-write", func(b *testing.B) {
		b.ReportAllocs()
		n := 0
		for b.Loop() {
			for i := range rows {
				key := HolderKey{Cache: CacheID(fmt.Sprintf("next-%d", n)), Number: uint64(i + 1)}
				if _, err := cloud.AttachRemoteHolding(ctx, key, benchmarkRerunRow(i)); err != nil {
					b.Fatal(err)
				}
				if _, err := cloud.AddRemoteHold(ctx, key, fmt.Sprintf("s%d", i/49)); err != nil {
					b.Fatal(err)
				}
			}
			n++
		}
		b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*rows), "ns/row")
	})
}
