# Chunk 4 cycle fix — FINAL review handoff (hardened, landable change)

The Chunk 4 implementer landed the hardened change as commit **e69d1f0049** (Erik-authored,
unpushed, NOT merged to main). This is the FINAL review before it merges into the
shared/validated `wcanalyze` replay (PR #13393 / native). The full commit diff is in
`wcprof-otel-chunk4-hardening-e69d1f0049.patch` (same dir) — review THAT. It touches
`engine/wcprof/wcanalyze/{replay.go,replay_cycle_test.go,report.go}` and
`engine/wcprof/wcotel/gate.go`. (If your git shares objects you can also `git show e69d1f0049`.)

## IMPORTANT: the landed mechanism is DIFFERENT from the skip-predicate you reviewed

You reviewed a *skip-predicate* (process a pre-spawn wait only if `endNS[target] ≤ startNS[spawn]`,
else skip). The implementer did NOT implement that. It found a reformulation:

**Sequence each gating wait at its recorded `EndNS` instead of its `StartNS`.** A single shared
`advance(op, stopAt)` interpreter then serves both the full finish (`stopAt<0`) and the
out-of-order prefix anchor `spawnTo` (`stopAt=child`). Because gating is end-ordered, a wait
still open at a spawn sorts AFTER the spawn, so the clock evolution up to any spawn is
byte-identical on both paths → order-independence BY CONSTRUCTION (not by per-cutoff recompute or
dual skip-predicates). This collapses hardening items #1 (path-dependence), #2 (thread the wait's
own `EndNS`, not the target-end proxy), and #3 (fixed waits) into one mechanism. `actionRank`:
gating-wait=0 < self=1 < spawn=2 < noop=3 (so `waitEnd == spawn ⇒ gated`).

So: equivalent in INTENT to what you endorsed, but a DIFFERENT mechanism none of you have
reviewed. That is exactly why this final review matters — vet the mechanism itself, not the intent.

## The implementer's own report (verbatim)

> **The centerpiece (item #1):** Tractable, ~170 lines, shape (b), confident — no escape hatch.
> I did *not* implement the literal "skip-predicate in both loops." I found a cleaner
> reformulation that IS the unified `advanceTo(op, cutoff)`, but achieves order-independence *by
> construction* instead of by per-cutoff recompute: **sequence each gating wait at its recorded
> END.** Then one `advance(op, stopAt)` interpreter serves both paths. Since gating is
> end-ordered, the clock evolution from an op's start up to any spawn is byte-identical on the
> prefix and full-finish paths — a wait still open at the spawn simply hasn't been reached yet on
> either path. So the child's anchored start cannot depend on which path reaches it first.
> First-write-wins is no longer load-bearing; `SimStartConflicts` proves it (0 on native 86k and
> OTel 11k).
>
> Why confident rather than nervous about touching validated native code:
> - Provably equivalent to the old model EXCEPT for spawns-during-waits — parent finishes are
>   unchanged because self-segments never overlap a wait interval, so the same self lands
>   before/after the gate either way. That's why native makespan is bit-for-bit −0.1% before and
>   after.
> - The full existing native suite passes untouched; `git stash`-ing the diff proved the OLD code
>   cycled on the native dump too (5 cycles/11 fallbacks) — not OTel-papering; it fixes the native
>   bug.
> This reformulation collapses #1/#2/#3 into one mechanism: end-ordering threads the wait's own
> recorded end (not the target-end proxy) and gives fixed-delay overlap the same treatment, with
> inclusive `waitEnd == spawn ⇒ gated` via sort rank (no `joinEpsilonNS` reuse).
>
> **Push back:** the oracle's `jaccard ≥ 0.80` bar is the WRONG gate for buildkit-heavy workloads.
> A reviewer treating the 0.15 I measured as a cycle-fix regression would be wrong on two counts:
> (a) jaccard ranks top-N by *self-time* (loader-computed) — the replay change CANNOT move it;
> (b) it's the inherent by-design 2nd-source difference (OTel buckets engine work as buildkit
> span-names `:uploading`/`:stdout`; native uses semantic classes `Host.directory`/
> `exec.processRun`). OTel is a second source, NOT parity. Holding it to 0.80 on an exec workload
> mismeasures the source.
>
> **Implemented:** `compileProgram` (gating waits sequenced at `w.EndNS`; `actionRank`
> gating-wait=0<self=1<spawn=2<noop=3); `advance(op, stopAt)` shared interpreter (was the body of
> `finish`); `spawnTo(par,target)` prefix-stop anchor (recurses to anchor ancestors; counts
> genuine in-flight/cross-root fallbacks via `fallbackAnchor`); `setStart` keeps first-write-wins
> but counts any *disagreeing* overwrite (`SimStartConflicts`); gate.go/report.go surface
> `start-conflicts`.
>
> **Measured (module workload):** cycles native 5→0, OTel 5→0; fallback-anchors native 11→0,
> OTel 18→0; OTel gate FAIL→PASS; `SimStartConflicts=0` both. config-parse what-if saves exactly
> 100ms. Drift: native −0.1% (unchanged), OTel −2.4%. Full native suite passes uncached. 86k-op
> native replay (baseline + 3 factors × ~200 classes) in 0.28s/174MB; OTel 11k in 0.11s; fan-out
> test 64 out-of-order anchors no blow-up. Self-time spot-check: pure user `call_exec` classes
> match native exactly (`Directory.digest` 0.00, `Query.moduleSource` 0.03, etc.); "§3.1 self-time
> faithful where there's no suppressed-caller fold."
>
> **Revises my own prior notes:** (1) the earlier "−0.3% / `ModuleSource.asModule` → 376ms" was
> the throwaway's *residual over-serialization* (save 376ms > its 258ms self-time = still
> spuriously blocking). The hardened model gives `asModule` save==self on OTel — structurally
> matching native (save==self). Hardened OTel makespan 5.96s sits closer to native's 5.86s than
> the throwaway's ~6.09s. So −2.4%-vs-actual is the *correct* order-independent compression. (2)
> `asModule`(0.40)/`Host.directory`(0.99) drifts are attribution not error: `Host.directory`
> native 781ms vs OTel call_exec 6.8ms because OTel puts the work in the child `:uploading` span
> (720ms) — total agrees, bucket differs.

## The lead's independent verification (already done — go DEEPER, don't redo)

I verified these myself against the code/commit:
- **The load-bearing invariant holds:** `SelfSegments()` (graph.go:381) subtracts every wait
  interval `{w.StartNS, w.EndNS}` from the op interval — so self-segments PROVABLY never overlap a
  wait. This is the basis for finish-invariance (end-ordering can't move a self-segment across a
  wait because there's none inside one).
- **Full `wcprof`/`wcanalyze`/`wcotel` suite passes** (ran it; green).
- **`TestGateFallbackAnchorsReportOnlyAndThreshold` passes UNMODIFIED** — the round-3 prediction
  it would break was wrong; the repurposed `FallbackAnchors` still counts the cross-root
  recorded-offset anchors the test constructs.
- **`replay_test.go` unmodified** and passes (native behavior preserved without touching its tests).
- The order-independence test (`TestConcurrentWaitOrderIndependent`) + the full battery exist.

The basics are solid. Your job is the DEEPER vetting of the NEW mechanism (charges in your prompt).
