package server

import (
	"fmt"
	"testing"
)

// Timing harness for the session's call payload store on the no-failure
// path. The A and B sides of the series share this file verbatim; only
// callpayload_bench_adapter_test.go differs, for the settle signature
// and for what a replay asks the store.

// BenchmarkCallPayloadDeliveryNoFailure runs one payload's whole no-failure
// life per op on the real session store: a walk claims it, the exporter takes
// it and settles it delivered, then a replay of the same call finds its root
// already claimed.
func BenchmarkCallPayloadDeliveryNoFailure(b *testing.B) {
	const n = 4096
	digests := make([]string, n)
	for i := range digests {
		digests[i] = fmt.Sprintf("xxh3:%016x", i)
	}
	route := []string{"child", "parent"}
	var sess *daggerSession
	var store *callPayloadDeliveryStore
	i := 0
	for b.Loop() {
		if i%n == 0 {
			sess = &daggerSession{}
			store = &callPayloadDeliveryStore{session: sess, targets: route}
		}
		dgst := digests[i%n]
		i++
		store.ClaimCallPayload(dgst)
		benchSettleDelivered(sess, dgst, sess.takeCallPayloadForWrite(dgst, route))
		benchReplay(store, dgst)
	}
}

// BenchmarkCallPayloadReplay is only the replay: the root is already claimed.
func BenchmarkCallPayloadReplay(b *testing.B) {
	sess := &daggerSession{}
	store := &callPayloadDeliveryStore{session: sess, targets: []string{"child", "parent"}}
	store.ClaimCallPayload("xxh3:root")
	for b.Loop() {
		benchReplay(store, "xxh3:root")
	}
}
