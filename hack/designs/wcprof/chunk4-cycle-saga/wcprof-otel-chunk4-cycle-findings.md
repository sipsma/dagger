# Chunk 4 cycle — empirical findings + proposed fix (TO BE EVALUATED CRITICALLY, NOT TRUSTED)

The Chunk 4 implementer investigated the module-loading cycle empirically (it has the captured 9.3MB OTel trace + the native dump + the live engine) and proposes a fix. **The owner's standing principles govern: unsoundness is a showstopper (no accommodations/seams, practical impact is irrelevant); a bug is a bug even if native shares it (fix it fundamentally in the whole system, don't scope out); empirical mindset (go where the evidence points, don't just prove a theory).** Per instruction, the implementer did NOT land the consequential fixes — it reports them for review. Your job: evaluate the findings AND the proposed fix critically — does it add up, is it correct, will it actually fix it, side effects, and **is it the FUNDAMENTAL fix or a paper-over?**

## Headline reframe (this CORRECTS the council's earlier theory)

The cycle is **NOT an OTel-emit faithfulness issue and NOT a false-nesting issue.** It is a **SHARED native+OTel bug in the `wcanalyze` REPLAY's anchor (start-ordering) mechanism**, hit by concurrent cross-referenced executions. Native wcprof produces the **identical 5 cycles** on the same workload. The proposed fix is therefore in the **shared replay (PR #13393, native's validated analyzer)** — which is exactly why it's flagged for review.

## Empirical findings, step by step (implementer's report)

- **Exp 1 — Is Chunk 3 stamping the attachment? NO.** Recompiled the captured trace with `wcprof.parent` **ignored** (raw `parentId`): cycles **5 → 8**, fallback anchors **18 → 21**. Cycles *persist and increase* without the stamping → Chunk 3's stamping is **not** the cause; it *reduces* cycles (as the Chunk 3 reviewer hypothesized). The false structure is in raw `parentId`.
- **Exp 2 — Topology: the council's "nested" theory is WRONG.** Mapped cycle ops to spans + traced raw chains. **The waiter (op#54, under `load module: go-sdk`) is NOT a `parentId`-descendant of the target (op#94, under `load module: go`)** — they are **concurrent *sibling* module loads under `POST /query`**, cross-referencing via TRUE singleflight waits. The cycle **requires the replay's `anchor` (start-ordering) edge** — a no-anchor SCC finds **0 cycles**. (So the earlier council mechanism — "op#54 is a detached descendant of op#94, the implicit join invents the back-edge" — is contradicted by the raw trace.)
- **Exp 3 — Does native cycle? YES, identically.** The native dump from the *same* run reports **"5 broken cycles, 11 fallback anchors."** Native builds parentage from its *own* wcprof context key (not OTel traceparent), yet cycles the same way → it's the shared detached-`call_exec` + concurrent-singleflight + **replay-anchor** model, not OTel emit.
- **Exp 4 — Emit re-root (the council's proposed fix direction) WORKS but is BAD.** Re-rooted every `call_exec` off the caller tree (keeping the true waits): cycles **5 → 0, gate passes** — confirming the false *nesting/anchor* is what cycles, not the wait. **But fallback anchors EXPLODED 18 → 3474** — it destroys the replay's start-anchoring. Empirically unacceptable.
- **Exp 5 — Root cause + clean fix.** The cycle is the replay's **anchor over-reach**: to get an out-of-order-referenced op's *start*, the current `finish(child)` path replays the **parent's full finish**, whose implicit join pulls in the concurrent cross-referencer that waits back → the back-edge. It only needs the parent's progress *to the child's spawn*. Prototyped a **start-only anchor**:

```go
func (s *Simulation) startOf(i int32) int64 {
    if s.started[i] { return s.simStart[i] }
    if par := s.p.parent[i]; par >= 0 {
        s.setStart(i, s.startOf(par)+(s.p.startNS[i]-s.p.startNS[par]))
    } else {
        s.setStart(i, s.p.startNS[i])
    }
    return s.simStart[i]
}
// in finish(i): replace the anchor block with:  if !s.started[i] { s.startOf(i) }
```

Result: **cycles → 0 for BOTH native and OTel; fallback anchors → 0** (improves vs re-root's 3474); makespan preserved (drift −0.3%); **`wcanalyze`'s own counterfactual tests + all chunk2/3/4 oracle/ranking fixtures pass** (no normal-case regression). Only `TestGateFallbackAnchorsReportOnlyAndThreshold` fails — it asserts the *old* behavior (cross-root wait-joins produce fallback anchors), which the fix removes.

## Proposed fundamental fix (for review — NOT landed)

The **start-only anchor in the shared `wcanalyze` replay**. Implementer's claim: it fixes the root cause in one place for both sources, preserves the true singleflight waits (no loader suppression/reclassification — the hard constraints hold), and is *not* papering over bad data (the data is faithful; the replay's anchor was over-reaching for concurrent work). It only changes the *out-of-order* anchor case; in-order timing is untouched.

Trade-offs the implementer flagged:
- It touches the **shared replay (PR #13393), so it changes native** — a design-level change (the design's premise was "reuse the validated native replay UNCHANGED").
- It **zeroes the `FallbackAnchors` §6.1 signal** (a soft "detached/re-pointed work" diagnostic). A refinement could keep a counter when `startOf` handles an out-of-order reference; `TestGateFallbackAnchorsReportOnlyAndThreshold` then needs updating.
- The **emit re-root** is the alternative if you'd rather keep the replay frozen — but it's empirically worse (3474 fallback anchors).

## service.start erasure (council finding #1) — VERIFIED REAL, also shared

Empirically confirmed: a 50ms `service.start` window `[10,60]` loads with **self-time = 2ms** (the availability child absorbs it) and ranks with **SavedNS = 1ms** → a slow service start does **NOT** headline as `service.start`. (The fresh-Codex reviewer, the 1/6 minority, was right.) **Shared with native** (code-confirmed): native's `service.start` op also has the daemon `exec.run` as a child (begun under `svcCtx`), so native's `service.start` self is erased too. A **§3.4 design gap** (the design said "leave the availability span non-self-time-bearing" but didn't prevent it *absorbing* `service.start`'s self). Proposed fix: re-root the long-lived availability (serviceSpan/daemon) **out of** `service.start` in **both** sources (an OTel-only re-root would diverge from native + break the oracle). Reported for review.

## Landed this turn

The **lazy-triggered-exec composition test** (commit `8c331d8272`) — asserts `exec.run` (direct re-pointed child) is stamped `wcprof.parent`=lazy op while its phases (descendants) are unstamped, the loader re-homes the exec subtree under the lazy op with `work_type=user` surviving, gate green. (The cycle fix + service fix are NOT landed — reported above for review.)

## Evidence access (for reviewers)

The raw artifacts (`/tmp/otel-exec.jsonl` 9.3MB cycle trace; `/tmp/native-exec.dump` the native dump that also cycles; `/tmp/otel-svc.jsonl`) live in the **implementer's container** — not directly accessible to you. **But the load-bearing claims are code-verifiable:** the replay's anchor mechanism is in the shared `engine/wcprof/wcanalyze/replay.go` (read it to confirm the "anchor replays the parent's full finish to get a child's start" over-reach, and to evaluate whether the `startOf` start-only fix is correct, fundamental, and side-effect-free). Reason from the findings + the replay code; if you judge you need the extracted cycle subgraph or the native cycle output to verify the empirical claims (Exp 2 siblings-not-nested, Exp 3 native-cycles), say so and the lead will have the implementer extract them.
