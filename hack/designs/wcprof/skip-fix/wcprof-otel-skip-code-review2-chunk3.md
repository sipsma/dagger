# Convergence re-review (code review round 2) — `4921d53662` — Chunk 3 implementer (lazy / wcprof.parent owner)

**Reviewed:** `git diff 4585bf413d..4921d53662` (net vs baseline) + `c18b17fc53..4921d53662`
(delta since round 1) + resulting files, in the coder worktree. file:line are that worktree.
Review only — did not modify the branch.

## SIGN-OFF

**I sign off on convergence. No remaining blocker.** My round-1 headline ask — the lazy
emit-path test pinning the N3 invariant — is satisfied by two real, concurrent, `-race`-clean
tests, and my round-1 distinct-from-invalid gap is closed by a third. The native un-gate
(Erik's ruling) is complete and reverts native to byte-for-byte baseline; the OTel gating —
including the lazy producer-flag gating I own — is intact and unperturbed. Details per (a)–(e).

---

### (a) Native un-gate — COMPLETE and correct

The cleanest proof is the **net diff vs baseline** (`4585bf413d..4921d53662`): on the native
side it adds **only comments**. Every round-1 native gate is reverted to the baseline
condition — verified each:
- outer `OpKindCall` (getOrInitCall :3603): `if !wcprof.Enabled(ctx) || req == nil ||
  req.ResultCall == nil` — no `|| ProfileSkip`. ✓
- native `execOp` (:3768): `if wcprof.Enabled(ctx)` — no `&& !ProfileSkip`. ✓
- native `pubOp`: not in the diff → unchanged baseline (`if oc.profOpID != 0`), and since
  `execOp` is minted again it follows. ✓
- native singleflight `BeginWait` (:3995): `if wcprof.Enabled(ctx)` — no `&& !oc.profSkip`. ✓
- native lazy op (:3031): `if wcprof.Enabled(evalCtx)` — no `&& !producerSkip`. ✓
- native lazy leader `BeginWait` (:3135): unconditional `profWait := wcprof.BeginWait(...)`. ✓
- native lazy joiner `BeginWait` (:2985): unconditional. ✓

So `ProfileSkip`/`producerSkip`/`oc.profSkip` reach **no native gate**. Native records the
reflection/introspection class exactly as the merged baseline — consistent with §9 (native
dump now contains 53,433 reflection-class ops, `dropped_events=0`: native is in-memory, not
BSP-constrained, so full detail costs nothing it can't afford). This is the right call:
native is dev-only/opt-in and was validated at PR#13393; leaving it untouched is zero-risk,
and the OTel second source (the volume-constrained, always-on path that actually overflowed
the BSP) is where the skip belongs.

### (b) OTel side intact + lazy producer-flag gating still correct — no perturbation, no dangle

The OTel gates are exactly the round-1 (verified-correct) logic, unchanged: OTel `call_exec`
(:3781 `&& !req.ResultCall.ProfileSkip`), OTel singleflight wait (:4013 `if !oc.profSkip`),
OTel lazy span (:3058 `&& !producerSkip`), OTel lazy joiner wait (:3006 `if !producerSkip`),
OTel leader wait follows `lazySpan != nil`.

**Un-gating native does not perturb the OTel lazy gating, because the two paths use disjoint
target state:** native keys on `shared.lazyEvalProfOpID` (set whenever native is enabled);
OTel keys on `shared.lazyEvalSpanCtx` (set only when `OTelProfActive && !producerSkip`). I
traced both attempt and joiner paths for a skipped producer: native mints its lazy op + waits
(full detail) via `lazyEvalProfOpID`, while OTel mints no span (`lazyEvalSpanCtx` stays the
reset-invalid zero, :3034) and emits no OTel wait — independent, no interference. The
`wcprof.parent` override is set only inside `beginOTelLazyOp` (gated off when skipped), so it
still can never point into the skipped set, and the deferred work's OTel spans re-home to the
forcer's present ancestor. **No cross-recipe OTel dangle** in any interleaving: an OTel dangle
needs `producerSkip==false` + invalid OTel target, which can only be the genuine untraced
case (correctly a loud targetless wait), never skip-induced (skip ⟹ producerSkip true ⟹ no
OTel wait). The round-1 LOW (joiner reads `producerSkip` after `lazyMu.Unlock()`) is unchanged
and still benign — and now exercised concurrently under `-race` by Test 2.

### (c) The three new tests — ADEQUATE; they genuinely exercise the flagged cases

All three drive the **real** lazy path (`Cache.Evaluate → evaluateOne`) with a real `Cache` +
recording tracer (incl. `NewWcprofLazyParentProcessor`) and assert on the **compiled** graph
(`wcotel.Compile → wcanalyze.Build → CheckStructural`), not synthetic spans:

- **`TestProfileSkipGatesLazyEmit`** (skipped vs kept producer): confirms frame-homing
  (`result.cacheSharedResult().loadResultCall().ProfileSkip == tc.profileSkip`), then asserts
  a skipped producer emits **0** lazy OTel spans and **0** lazy waits while a kept one emits
  both, gate clean each way. Real producer-flag gating, basic case. ✓
- **`TestProfileSkipLazyCrossRecipeForcerStaysClean`** — the N3 case, and it genuinely
  exercises forcer≠producer: a **skipped** producer whose pending value is forced by a
  **traced joiner that actually joins the in-flight eval** — the test parks the leader in the
  callback and spins until `shared.lazyEvalWaiters >= 2` before releasing, so it is provably
  the concurrent joiner-wait path, not a sequential stand-in. It asserts 0 lazy spans, 0 lazy
  waits, **0 `UnresolvedWaitTargets`**, gate passes, and the comment names the exact
  counterfactual (gating on the joiner's own bit would dangle). This is precisely the invariant
  I raised. ✓
- **`TestProfileSkipDoesNotBlindInvalidTargetDetector`** (§8.5): a **non-skipped** producer
  with an **untraced leader** (so `lazyEvalSpanCtx` is genuinely invalid) forced by a traced
  joiner → asserts `UnresolvedWaitTargets > 0` and the gate **fails loud**. Proves the skip bit
  did not blind the mixed-recording detector and locks out a future "gate on `execSpanCtx`
  validity" refactor. ✓

Both my round-1 test gaps (lazy emit; distinct-from-invalid) are closed by real tests; the
summary reports all pass under `-race`, consistent with the disjoint-target-state analysis.

### (d) Doc/comment corrections — right and complete

- `profileSkip` doc-comment (telemetry.go) now reads "the wcprof **OTel second source** must
  skip" and "Native wcprof is opt-in / dev-only … **NOT gated by this predicate**," and states
  refinement-3 precisely: a directly-called reflection accessor keeps its `dag.call` UI span
  (a coarse OTel `"call"` op) while the OTel source skips only `call_exec`; "Native keeps the
  full op, so this is **NOT source-parity** … the cross-source oracle compares only the
  non-reflection classes." ✓
- `ResultCall.ProfileSkip` field comment now says "the wcprof **OTel second source** must NOT
  profile this call … (Native wcprof … ignores this bit) … Only the OTel emit gates in cache.go
  read it." ✓
- `grep` finds **no** stale "BOTH the native"/"native recorder nor"/"both sources" skip claims.
  The cache.go gates carry accurate "native: full detail, not profile-skip-gated" comments. ✓

(Cosmetic only, non-blocking: the inserted parenthetical in the `ProfileSkip` field comment
left one over-long unwrapped line — a `gofmt`/lint pass will not touch comment wrapping, so
it's harmless; tidy if convenient.)

### (e) Remaining blockers — NONE

No blocker. Additional notes, none blocking:
- **My round-1 telemetry-off+native-on caveat is now MOOT.** With native un-gated it always
  profiles regardless of `ProfileSkip`, and OTel only emits under an active recording span
  (where `AroundFunc` stamps the bit) — so that corner no longer has an inconsistency.
- **Oracle consequence (documented, expected):** the cross-source oracle is now "non-reflection
  classes only" — native carries reflection detail the OTel source intentionally lacks. This is
  correctly stated in-code, and the wcotel oracle unit fixture is unaffected (it predates the
  reflection `call_exec` emit; zero loader/replay change re-confirmed — the diff touches no
  `wcanalyze`/`wcotel` file). The load-bearing user-work parity the oracle exists for is
  unchanged.
- **Carryover NOTE (still acceptable):** the over-cut "no reflection-type field forces real
  work" assumption rests on §9.4 + the name-trap exclusions; keep §9.4 a recurring merge gate.

**Converged. Land it.**

**(Carried, separate):** service.start §3.4 self-erasure re-root still owed in both sources.
