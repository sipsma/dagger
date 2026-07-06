# Skip-fix convergence re-review (code review round 2) — design author, commit `4921d53662`

Reviewed the amend (`git diff c18b17fc53..4921d53662`) and the net change
(`4585bf413d..4921d53662`) in the coder worktree. Verified at file:line.

## Verdict: SIGN OFF on convergence.

Both required round-1 changes are correctly and completely applied; I found no
remaining blocker. The native un-gate is not just correct — it's a **de-risking**: native
reverts to the `4585bf413d` baseline, so the "this modifies validated PR #13393 native
code" concern from round 1 is **eliminated**, and the volume fix is now cleanly scoped to
the OTel second source where the problem actually lives. The two new tests close both of
my round-1 MEDIUMs with rigorous concurrent coverage. UI-isolation, the call-site stamp,
digest-exclusion, frame-homing, and zero-loader/replay all still hold.

## (a) Native un-gate — COMPLETE and correct

Every native gate from round 1 reverted to baseline (verified in the amend delta):
- N1 outer `OpKindCall` (getOrInitCall :3603-3610) → `if !wcprof.Enabled(ctx) || req==nil
  || req.ResultCall==nil` (the `|| req.ResultCall.ProfileSkip` removed).
- native `execOp` (:3766) → `if wcprof.Enabled(ctx)`.
- native singleflight `BeginWait` (c.wait :3994) → `if wcprof.Enabled(ctx)`.
- native lazy op (:3031) → `if wcprof.Enabled(evalCtx)`.
- native lazy joiner wait (:2982) and leader wait (:3135) → unconditional `wcprof.BeginWait`.
- native `pubOp` follows the now-un-gated `execOp` (emitted for reflection). ✓

`ProfileSkip`/`oc.profSkip`/`producerSkip` are retained but now read **only** by the OTel
gates. §9 confirms the intended effect: the native dump again contains the reflection
class (53,433 ops, `dropped_events=0`, loads in `wcanalyze`). Crucially, native is a local
dump on a **separate pipeline** from the OTel→BSP→Cloud export, so restoring 53k native
ops does **not** re-introduce the BSP overflow — the volume fix stays intact on the OTel
side. Complete and correct.

## (b) OTel side — untouched, still correct, UI still isolated

The amend did **not** touch any OTel gate; I confirmed each remains gated:
- OTel `call_exec` (:3784) → `if OTelProfActive(callCtx) && !req.ResultCall.ProfileSkip`.
- OTel `publishResult` → follows `execSpanCtx.IsValid()`.
- OTel singleflight wait (:4016) → `if !oc.profSkip { EmitOTelWait }`.
- OTel lazy span (:3059) → `if OTelProfActive(evalCtx) && !producerSkip`; OTel lazy
  joiner/leader waits → gated on `producerSkip`/`lazySpan`.

So the OTel graph's **dangle-safety by same-flag construction is unchanged** (call_exec +
waits gate on the same `ProfileSkip`/`oc.profSkip`; lazy span + waits on the same producer
flag). §9 confirms: OTel 1646 call_exec + 1646 publishResult, **0 residual introspection,
gate 0/0**. UI-isolation holds — `introspectionInfo` is still behavior-preserved (only the
root-list→shared-map refactor; receiver switch untouched), and `dag.call` is untouched.

## (c) The two new tests — adequate, and rigorous

Both round-1 MEDIUM asks are closed with real concurrent (leader+goroutine+joiner, polled
to `lazyEvalWaiters>=2`, `-race`) emit-path tests:
- `TestProfileSkipGatesLazyEmit` — drives the real `Cache.Evaluate→evaluateOne` path;
  table case (skipped producer ⇒ 0 lazy OTel span + 0 lazy waits; kept ⇒ both present),
  asserts frame-homing (producer frame carries the bit) and gate 0/0. (my MEDIUM #2)
- `TestProfileSkipLazyCrossRecipeForcerStaysClean` — the load-bearing **N3** case: a
  skipped producer's pending value forced by a TRACED (different-recipe) joiner; asserts
  0 lazy span, 0 lazy wait, **`UnresolvedWaitTargets==0`**, gate passes. The comment names
  the exact failure it guards (joiner's-own-bit → targetless wait → dangle). (my MEDIUM #2)
- `TestProfileSkipDoesNotBlindInvalidTargetDetector` — §8.5: an UNTRACED leader (invalid
  `lazyEvalSpanCtx`) + a TRACED **non-skipped** joiner must STILL emit a targetless wait;
  asserts **`UnresolvedWaitTargets>0` and gate `Err()`** — the detector fires, proving the
  skip bit didn't blind it, and explicitly "locks out a future `gate on
  execSpanCtx.IsValid()` refactor." (my MEDIUM #1)

These are real regression guards, not token tests. And the OTel-only emit assertions
(`countOTelKind`/`countOTelWaitLinks`) mean none of the existing tests break under the
native un-gate (they never asserted native skipping).

## (d) Comments/docs + canonical alignment + zero loader/replay

- Comments corrected accurately: `profileSkip` doc, the `ProfileSkip` field doc, the
  AroundFunc stamp comment, and every gate comment now say "OTel-only; native keeps full
  detail." The N3 load-bearing lazy comment is preserved and re-scoped to "OTel side only."
- Refinement-3 stated precisely: a directly-called reflection accessor keeps its `dag.call`
  UI span (→ a coarse OTel `"call"` op) while the OTel source skips only its `call_exec`;
  NOT source-parity; "~always under a hideCtx so the divergence is empirically nil"
  (§9 divergence=0). This is self-consistent (the `dag.call` "call" op is a present parent;
  no dangle) and, being a reflection class, is excluded from the oracle anyway. ✓
- Zero loader/replay change preserved (amend touches only cache.go/telemetry.go/
  result_call_frame.go + tests; no `wcanalyze`/`wcotel`). Digest-exclusion, frame-homing,
  and the reflection-set completeness verified in round 1 are unchanged by this amend.

## (e) Remaining blocker? — None. Signed off.

One **non-blocking canonical reconcile** to record (a consequence of Erik's ruling, which
owns the tradeoff — not a code issue):

- **Oracle interpretation (§6.2/§6.4).** With native un-gated, the two sources are no
  longer structurally identical even on **non-reflection** classes: native keeps the
  reflection ops as children (their time subtracted from the ancestor's self), while the
  OTel source skips them so that time **folds into the kept ancestor's self**. So a
  non-reflection ancestor that triggers reflection (e.g. a module-load class) will show
  `OTel self > native self` by the folded reflection time. The implementer's "oracle
  compares only non-reflection classes" is necessary but not sufficient for per-class
  self-time parity on those ancestors. The fix is interpretation, not code: compare the
  bottleneck **ranking** (robust — reflection isn't a bottleneck) and/or fold-normalize the
  native side before comparing; treat ancestor self-time drift as **expected**, not drift.
  Worth a sentence in the canonical §6.2/§6.4. Severity LOW — Erik's ruling accepted this
  tradeoff (native full detail, OTel volume-safe), and the skip fix's correctness is
  independent of it.

The round-1 LOW items (fork-inheritance reverse edge; input-reconstructed frame
default-false; old pre-fix persisted data) are unchanged and remain bounded volume edges,
never dangles — accepted.

**Convergence: signed off.** The skip fix is landable — native un-gate complete and
de-risking, OTel side correct and still self-consistent (gate 0/0, 0 residual), both
round-1 test gaps closed with rigorous concurrent tests, UI isolated, digest-excluded,
zero loader/replay change. The only follow-up is the LOW canonical note on oracle
interpretation, which does not gate this merge.
