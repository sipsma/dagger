# Skip fix — convergence re-review (code review round 2), Chunk 2 implementer

Reviewed `4921d53662` (amended over `c18b17fc53`) against baseline `4585bf413d` in the
`…-skip-coder-daa3a9d2` worktree. Verified the two round-1 changes against the diff + code + a live
test run. File:line are that worktree.

## SIGN-OFF: CONVERGED. No remaining blocker.

Both required changes are correctly and completely applied. I ran the new tests under `-race` myself
(green). The native un-gate is complete (native is byte-for-byte baseline), the OTel skip is intact and
still correct, the frame-homing/digest-exclusion are behaviorally untouched, and the principle holds
(zero `wcanalyze`/`wcotel` change). Land it.

## (a) Native un-gate — COMPLETE and correct

The decisive check is the **net diff from baseline**: if native is truly reverted, no native-emit line
changes. Confirmed — `git diff 4585bf413d..4921d53662 -- dagql/cache.go` filtered to
`wcprof.Enabled | OpKindCall | profOpID | BeginOp | BeginWait | OpKindCallExec | OpKindLazy` returns
**nothing**. So every native gate is at exact baseline:
- outer `OpKindCall` (`:3610`) back to `!wcprof.Enabled || req==nil || req.ResultCall==nil` (no
  `|| ProfileSkip`);
- native `execOp`, `pubOp`, singleflight `BeginWait`, lazy `lazyOp`, lazy joiner/leader `BeginWait` —
  all unchanged from `4585bf413d`.
The in-code comments now state it correctly ("native is full detail, emits its leader wait
unconditionally"; "producerSkip drives ONLY the OTel …"). Native dump containing 53,433 reflection ops
with `dropped_events=0` is exactly baseline behavior — native is a dev-only HTTP-pulled dump, not the
always-on BSP path, so it never had the volume problem and correctly keeps full detail.

Frame-homing / digest exclusion untouched: the amendment's changes to `result_call_frame.go` and
`core/telemetry.go` are **comment-only** (verified: every changed line in those two files is a `//`
comment). The `ProfileSkip` field, `clone()`/`fork()` copies, and digest exclusion are unchanged.

## (b) OTel side — intact and still correct

Un-gating native did not perturb the OTel gating. The net-from-baseline diff shows the OTel gates exactly
as in round 1 (which I verified correct): OTel `call_exec` gated `&& !req.ResultCall.ProfileSkip`
(`:3784`), `publishResult` follows `execSpanCtx.IsValid()`, OTel wait `if !oc.profSkip` (`:4016`), lazy
OTel span `&& !producerSkip` (`:3059`), lazy OTel joiner/leader waits `if !producerSkip`. `oc.profSkip`
and `producerSkip` are retained and now feed **only** OTel; both are still computed
(`shared.profileSkip()` / `frameProfileSkip(resultCall)`), so no dead read and no broken OTel gate.
Shared-state stays consistent: a skipped reflection miss has a valid native `oc.profOpID` (native
un-gated) **and** an invalid `oc.execSpanCtx` (OTel skipped) — the native wait resolves to the native
op, the OTel wait is gated off; two independent, self-consistent graphs. §9 confirms the OTel side
clean (1646 call_exec, 0 residual, gate 0/0), and the tests below confirm the skip still works.

## (c) The new tests — adequate, and they pass under `-race`

I ran all three: `ok github.com/dagger/dagger/dagql 1.045s`.
- **`TestProfileSkipGatesLazyEmit`** — table-driven: skipped producer → 0 lazy span + 0 lazy waits +
  gate pass; kept producer → lazy span + waits emitted + gate pass. Asserts the producer frame carries
  the bit (frame-homing).
- **`TestProfileSkipLazyCrossRecipeForcerStaysClean`** — **exactly the N3 case I asked for in round 1.**
  A skipped producer's pending value is forced by a *traced, non-skipped* joiner (a different recipe),
  with a real concurrent leader+joiner that the test confirms actually joins (`lazyEvalWaiters >= 2`).
  Asserts `0` lazy spans/waits, **`UnresolvedWaitTargets == 0`**, and gate-pass — and the comment
  documents that keying on the joiner's own bit would dangle. This is the load-bearing regression guard
  the path was missing; it's well-constructed (real trace root, compiled through the wcotel loader).
- **`TestProfileSkipDoesNotBlindInvalidTargetDetector`** (§8.5) — non-skipped producer + an *untraced*
  leader (genuinely invalid `lazyEvalSpanCtx`) + traced joiner; asserts `UnresolvedWaitTargets > 0` and
  `res.Err() != nil` (gate fails loud). Proves the skip bit did not blind the mixed-recording detector
  and locks out a future "gate on `execSpanCtx.IsValid()`" refactor.

Both my round-1 asks (MED lazy emit-path test, LOW distinct-from-invalid test) are addressed with
correct, passing tests.

## (d) Doc/comment corrections — right

The symmetry comments are corrected to OTel-only, and refinement-3 is stated precisely and is sound: a
*directly-called* reflection accessor keeps its coarse OTel `"call"` op (the AroundFunc `dag.call`
span) while wcprof skips its `call_exec` — so OTel is **not** source-parity with native there, and the
cross-source oracle compares non-reflection classes only. That is correct: native and OTel record
user-work (non-reflection) identically, so comparing those validates the OTel attribution; the
reflection divergence is by-design and excluded.

## (e) Remaining blocker — none

Un-gating native is the conservative choice (leaves the merged PR #13393 native profiler exactly as
shipped) and introduces no new issue: native faithfully records reflection (rational analysis of
faithful data), OTel omits it for the always-on volume contract (a smaller but still self-consistent,
inference-free graph). I re-checked the shared-state interaction, the principle, and the goal — all
hold.

**One non-blocking note (process, not code):** with native keeping reflection and OTel dropping it,
any *future* cross-source oracle run must exclude the reflection class (refinement-3), or it will show
spurious per-class divergence. The oracle isn't part of this commit; just flag it for whoever next runs
the oracle so they don't misread the expected divergence. The earlier round-1 LOW cosmetic (fork()
comment wording) is immaterial to convergence.

**Verdict: converged — sign off, land it.**
