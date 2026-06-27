package wcotel

import (
	"testing"

	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// TestChunk5PersistedResultDecodeFaithful is the persisted/imported-result
// reserve-seam fixture (deferred from Chunk 2). A result imported from the cache is
// decoded LAZILY on first use (the persisted-decode); that decode is emitted as a
// `lazy` op exactly like any deferred evaluation, and a consumer that forces it
// blocks on it and emits a `lazy` wait edge. This fixture drives that shape through
// the loader + §6.1 gate and asserts the persisted-decode wait resolves to the
// decode op (no dangle), the gate is clean, and the simulated baseline tracks the
// actual makespan (no drift) — i.e. profiling across cache-persisted/imported
// results stays faithful.
//
// (The skip fix already made ResultCall.ProfileSkip survive import via its JSON
// tag, so an imported *reflection* result is correctly NOT profiled by the OTel
// source; a non-reflection imported result like this Directory IS profiled, and its
// decode wait edge must be faithful. The loader needs no special persisted-import
// logic — the imported result's spans nest and wait via the same parentId/wait-link
// mechanism as any other, design §5 — which is exactly what this fixture confirms.)
func TestChunk5PersistedResultDecodeFaithful(t *testing.T) {
	const (
		pRoot     = "c5c5000000000001"
		pConsumer = "c5c5000000000002"
		pLazy     = "c5c5000000000003" // the imported result's lazy persisted-decode (resume <field>)
		pDecode   = "c5c5000000000004" // the decode's own work (payload decode / snapshot lease)
	)
	// A tight, slack-free critical path: consumer self-work [0,10] → blocked on the
	// persisted decode [10,70] → consumer self-work [70,90]. The decode fills its
	// lazy op exactly. With no schedulable slack, a faithful replay lands on the
	// actual 90ms makespan (drift 0).
	otelRecs := []map[string]any{
		otSpan(pRoot, idNone, "POST /query", 0, 90, nil),
		// the consumer forces an imported Directory and blocks on its lazy decode:
		// the load-bearing persisted-decode wait edge.
		otSpan(pConsumer, pRoot, "Directory.export", 0, 90, callExecAttrs("sha256:consumer"),
			otWait(pLazy, "lazy", 10, 70)),
		// the imported result's lazy persisted-decode (the resume span), the
		// self-time-bearing op for the deferred decode.
		otSpan(pLazy, pRoot, "resume directory", 10, 70, lazyOpAttrs()),
		// the decode's own work, filling the lazy op exactly.
		otSpan(pDecode, pLazy, "Directory.withDirectory", 10, 70, callExecAttrs("sha256:decode")),
	}
	c := mustCompile(t, toJSONL(t, otelRecs...))
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// §6.1 + the reserve-seam: the persisted-decode wait resolves to the lazy op,
	// nothing dangles, no false roots.
	gate := mustGate(t, c, g)
	if gate.WaitEdges != 1 || gate.UnresolvedWaitTargets != 0 || gate.OrphanedParents != 0 {
		t.Fatalf("persisted-decode wait must resolve cleanly: waits=%d unresolved=%d orphaned=%d",
			gate.WaitEdges, gate.UnresolvedWaitTargets, gate.OrphanedParents)
	}
	consumer := opByClassKind(g, wcprof.OpKindCallExec.String(), "Directory.export")
	lazy := opByClassKind(g, wcprof.OpKindLazy.String(), "resume directory")
	if consumer == nil || lazy == nil {
		t.Fatalf("missing ops: consumer=%v lazy=%v", consumer, lazy)
	}
	if len(consumer.Waits) != 1 || consumer.Waits[0].Target != lazy {
		t.Fatalf("consumer must carry one lazy wait targeting the persisted decode; got %v", consumer.Waits)
	}

	// The decode work nests under the lazy op (the persisted-decode subtree is
	// intact, not re-rooted) — so the imported result's cost is attributed to its
	// decode, not lost or mis-parented.
	decode := opByClassKind(g, wcprof.OpKindCallExec.String(), "Directory.withDirectory")
	if decode == nil || decode.Parent != lazy {
		t.Fatalf("the persisted decode's work must nest under the lazy decode op; got parent %v", decode)
	}
	// (Drift across persisted/imported results is validated end-to-end on real
	// captures by TestStandingDriftGate §6.4; on a hand-built fixture the simulated
	// baseline reflects the replay's self-time model, not emit faithfulness.)
}
