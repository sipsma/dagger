# wcprof × OTel — the module-loading cycle: fundamental root cause + fix (empirical)

**Author:** Chunk 4 implementer. **Status:** investigation + validated fix proposal
(NOT landed — per Erik's ruling, the real fix lands later under review).
**Charge:** Erik ruled `startOf` (recorded-offset anchor) OFF THE TABLE because it
gives a wrong counterfactual. This doc pins the *exact* cycle, tests Approach 2
(prefix-spawn) empirically, finds + resolves the residual, and proposes the
fundamental fix. All numbers are measured on the captured real trace
(`/tmp/otel-exec.jsonl`, 11,162 ops) + the native dump from the same run
(`/tmp/native-exec.dump`, 86,192 ops) + two minimal fixtures.

---

## TL;DR (the owner-decision summary)

- **Exact minimal cycle:** four *concurrent* `Query.moduleSource` call_execs (the
  dagger repo's modules loaded in parallel) cross-reference via **TRUE singleflight
  waits** (A→X, B→D, C→D, D→A). All wait edges are real (recorded JOINs). The loop
  is closed by **TWO `anchor` over-reaches**: `finish(A)` needs X's start → the
  anchor does a *full* `finish(B)` (B's full replay waits on D) → `finish(D)` does a
  *full* `finish(C)` → C/D resolve back to A (in-flight). The over-reach is the bug:
  the anchor replays a parent's **whole finish** to get a child's **start**, pulling
  in the parent's *later* cross-reference waits. **Native cycles identically** (5/5)
  — it is a shared replay-model bug, not an OTel emit bug.
- **Approach 2 (prefix-spawn) empirically REMOVES the cycle AND gives the right
  answer.** Replaying the parent *with the factor* only up to where it spawns the
  target (then stopping) breaks the over-reach. On the config-parse ground-truth
  fixture it returns the **correct 100 ms saving** (same as the current replay,
  unlike `startOf`'s wrong 0 ms).
- **It still looped once** (Erik's "open worry" realized): a residual cycle where
  the parent's *concurrent* wait *started before* it spawned the target but *ended
  after*. **Resolved principledly by the §1.5 recorded-back-edge rule**: a prefix
  wait that **outlasted the target's recorded spawn** was concurrent with the spawn
  and did not gate it, so it must not be serialized. With that one extra check:
  **cycles = 0 on BOTH native and OTel**, fallback-anchors = 0, makespan preserved
  (drift −0.3 %), all chunk2/3/4 oracle/ranking fixtures pass.
- **This confirms Erik's seed hypothesis:** unify the anchor path with the other
  two — **anchor = minimal scaled prefix-to-spawn, residual cycles resolved by the
  one recorded-back-edge mechanism.**
- **What the owner must decide:** (1) accept this as the fundamental fix (it is a
  change to the **shared `wcanalyze` replay**, so it touches native + PR #13393);
  (2) the `FallbackAnchors` §6.1 signal goes to 0 under the fix (prefix-spawn always
  anchors) — keep a refined counter or retire the signal; (3) one
  `wcotel` gate test (`TestGateFallbackAnchorsReportOnlyAndThreshold`) asserts the
  old fallback behavior and needs updating.

---

## 1. The exact minimal cycle (real trace, RING 1)

Instrumented `finish()` with a top-pushed recursion stack to extract the exact
ring (ops + edge-type + wait classification). RING 1, annotated:

```
finish(A=op#94  call_exec Query.moduleSource ident=…5959c354  [160..195]ms)  [A in-flight]
  A waits X=op#91 (singleflight [176..190] -> JOIN)         => finish(X)
   finish(X)            -> X unstarted, ANCHOR -> finish(op#70) -> finish(B=op#28) [FULL]
    finish(B=op#28 call_exec …7371c40c [148..213])
      B waits D=op#54 (singleflight [160..205] -> JOIN)     => finish(D)
       finish(D=op#54)  -> D unstarted, ANCHOR -> finish(C=op#47) [FULL]
        finish(C=op#47 call_exec …bec97f5f [149..243])
          C resolves/joins D ; D waits A (singleflight [176..195] -> JOIN) => finish(A)
            finish(A)    -> A IN-FLIGHT  => CYCLE
```

Key facts (all measured):
- **All four wait edges are TRUE recorded JOINs** (waitEnd ≥ targetEnd−ε): A→X,
  B→D, C→D, D→A. Dropping any of them re-introduces Break #1 — they are not the bug.
- **The waiter is NOT nested under the target** (revises the council's "joiner under
  target" theory): A is under `load module: go`; D is under `load module: go-sdk`;
  they are *concurrent sibling subtrees* under `POST /query`.
- **The cycle requires the ANCHOR edge.** A no-anchor SCC (join + wait-join only)
  finds **0 cycles**. The two `anchor` hops (X→…→B, D→C) are load-bearing.
- **The wrinkle Erik asked to identify:** B and C are in **sibling subtrees that are
  NOT yet in-flight** when A's replay references their members, so the anchor path
  (`finish(par)`, which only fires when the parent is not in-flight) does a **FULL**
  finish of them — pulling in their *later* cross-reference waits.

### Minimal fixture (reproduces with 9 ops)

`engine/wcprof/wcanalyze/cycle_fixture_test.go` `buildMinCycleGraph` — root with
three sibling subtrees pA(end 400) < pB(500) < pC(600); pA→A, pB→B(→X), pC→C(→D),
with waits A→X, B→D, D→A (all JOIN). Measured: **current replay CycleWarnings=1**;
the fix below → **0**.

```
root[0,1000]
 ├ pA[10,400] ─ A[100,200]            A → X (singleflight, JOIN)
 ├ pB[20,500] ─ B[100,400] ─ X[150,180]   B → D (singleflight, JOIN)
 └ pC[30,600] ─ C[100,400] ─ D[150,350]   D → A (singleflight, JOIN, closes)
```

---

## 2. Why `startOf` is wrong, confirmed empirically (config-parse fixture)

`buildConfigParseGraph` (Erik's textbook case): `app` does a 100 ms `config-parse`
(scaled child op) then spawns `T`; `lib`'s `W` dedups onto `T` (cross-tree wait);
`lib` ends before `app` so `W` references `T` **before** `app` replays → the **anchor
path fires**. Ground truth for `config-parse → 0`: `T` spawns at 0, finishes 100 ms
earlier, `W` and `app` finish 100 ms earlier ⇒ **makespan saves exactly 100 ms**.

Measured `saved = baseline_makespan − scaled_makespan`:

| approach | config-parse saved | correct? |
|---|---|---|
| **current replay** (full-finish anchor) | **100 ms** | ✅ (anchor replays app WITH the factor) |
| `startOf` (recorded-offset anchor) | 0 ms | ❌ (freezes T at app.start+100 regardless of factor) |
| **Approach 2** (prefix-spawn) | **100 ms** | ✅ |

So the current replay is **already correct on the textbook case** — the *only* defect
is the cycle. `startOf` would regress this to 0. Approach 2 keeps it correct.

---

## 3. Approach 2 (prefix-spawn) + the residual + its principled resolution

**Approach 2:** in `finish(i)`, replace the over-reaching anchor `s.finish(par)`
with `s.spawnTo(par, i)` — replay `par` *with the factor* only up to the action
that spawns `i`, committing children's starts as it goes, then **stop** (don't
finish `par`, so its later cross-reference waits are never reached). `par`'s own
finish runs later, in order.

Measured: **minimal fixture 1→0**, **native real trace 5→0**, config-parse still
100 ms. **But the OTel real trace still had 5 cycles** — the residual (RING, len 4):

```
finish(A=op#94) -> A waits op#251 (ModuleSource.withName, ZERO-DURATION [190..190], JOIN)
  finish(op#251) -> spawnTo(op#250, op#251) -> a PREFIX wait hits D=op#54  (spawnTo-wait)
    finish(D) -> D waits A -> finish(A) [in-flight] => CYCLE
```

**Root of the residual:** op#251's parent op#250 has a **concurrent** wait on D that
*started* before it spawned op#251 (190 ms) but *ended* after (D ends 205 ms). In the
recording op#251 spawned at 190 **while** op#250 was still waiting on D — so D did
**not** gate op#251's spawn. But naive prefix-spawn processes any wait whose *start*
precedes the spawn, serializing the concurrent D-wait and re-closing the loop.
(Native avoids this because it records a separate `call` op per caller; OTel's §3.1
suppressed-caller-wait-on-ancestor rule attributes the concurrent wait to op#250's
call_exec span, which is what surfaces it here. The residual is OTel-specific only in
*where the wait is attributed*; the underlying over-serialization is general.)

**Principled resolution (the §1.5 recorded-back-edge rule):** a prefix wait that
**outlasted the target's recorded spawn** was concurrent with the spawn and did not
gate it. Skip it. Only waits that *finished by* the target's recorded spawn actually
gated it. This uses the recorded temporal ordering as ground truth — the same family
as the existing `actWaitNoop` classification (waitEnd < targetEnd ⇒ abandoned).

With that single extra check: **OTel cycles 5→0, native 0, both gates PASS,
fallback-anchors=0, makespan drift −0.3 %, 0 residual rings, RunWhatIfs 0.18 s.**

---

## 4. The fundamental fix (proposed code; NOT landed)

In `finish(i)`, the anchor block becomes simply:

```go
if !s.started[i] {
    s.spawnTo(s.p.parent[i], i)
}
```

New helper (replaces the full-finish over-reach AND the recorded-offset fallback —
they unify into one scaled prefix-to-spawn):

```go
// spawnTo replays op `par` WITH the per-class factor only up to the action that
// spawns `target`, committing each child's simulated start, then STOPS — so the
// target gets its correctly-scaled start without par's LATER cross-reference waits
// being pulled in (the anchor over-reach + cycle). par is left started-but-
// unfinished; its own finish() runs later in order (idempotent: spawnTo commits
// only children's starts, and finish(par) recomputes the same prefix clock).
func (s *Simulation) spawnTo(par, target int32) {
    if par < 0 { s.setStart(target, s.p.startNS[target]); return }
    if !s.started[par] {
        if pp := s.p.parent[par]; pp >= 0 && !s.inFlight[pp] { s.spawnTo(pp, par) }
        if !s.started[par] { // par's own parent in-flight: recorded-offset fallback
            anchor := s.p.startNS[par]
            if pp := s.p.parent[par]; pp >= 0 && s.started[pp] {
                anchor = s.simStart[pp] + (s.p.startNS[par] - s.p.startNS[pp])
            }
            s.setStart(par, anchor)
        }
    }
    if s.inFlight[par] { // par mid-replay (prefix self-ref): recorded offset
        s.setStart(target, s.simStart[par]+(s.p.startNS[target]-s.p.startNS[par])); return
    }
    s.inFlight[par] = true
    defer func() { s.inFlight[par] = false }()
    clock := s.simStart[par]
    factor := s.factorOf[s.p.classOf[par]]
    pendCur, pendEnd := s.p.pendOff[par], s.p.pendOff[par+1]
    joinUpTo := func(t int64) {
        for pendCur < pendEnd {
            c := s.p.pendIdx[pendCur]
            if s.p.endNS[c] > t { return }
            pendCur++
            if !s.started[c] { s.setStart(c, clock) }
            if f := s.finish(c); f > clock { clock = f }
        }
    }
    for ai := s.p.actOff[par]; ai < s.p.actOff[par+1]; ai++ {
        a := s.p.actions[ai]
        joinUpTo(a.at)
        if a.kind == actSpawn && a.ref == target { s.setStart(target, clock); return }
        switch a.kind {
        case actSelf:
            clock += int64(float64(a.dur) * factor)
        case actSpawn:
            s.setStart(a.ref, clock)
        case actWaitJoin:
            // recorded-back-edge: only waits that FINISHED by target's recorded
            // spawn gated it; a wait that outlasted the spawn was concurrent.
            if s.p.endNS[a.ref] <= s.p.startNS[target] {
                if f := s.finish(a.ref); f > clock { clock = f }
            }
        case actWaitFixed:
            clock += a.dur
        }
    }
    s.setStart(target, clock)
}
```

This unifies Erik's three `finish()` entry paths: **anchor = scaled prefix-to-spawn**
(no over-reach), and **any residual cycle is broken by the recorded-back-edge rule**
(concurrent prefix waits don't gate the spawn) — the same recorded-ordering principle
the wait-join path already uses for `actWaitNoop`.

---

## 5. All measured numbers

| metric | current replay | Approach 2 (+back-edge) |
|---|---|---|
| minimal cycle fixture CycleWarnings | 1 | **0** |
| config-parse saved (truth=100ms) | 100 ms ✅ | **100 ms ✅** |
| OTel real trace cycles | 5 (gate FAIL) | **0 (gate PASS)** |
| OTel real fallback-anchors | 18 | **0** |
| OTel real makespan drift vs actual | −0.1 % | **−0.3 %** (≈preserved) |
| native real trace broken cycles | 5 | **0** |
| native real fallback-anchors | 11 | **0** |
| native real makespan drift | −0.1 % | −0.1 % |
| module-free exec / service traces | PASS / 0 cyc | **PASS / 0 cyc** (no regression) |
| chunk2/3/4 oracle + ranking fixtures | pass | **pass** (no normal-case regression) |
| RunWhatIfs wall time (11k ops) | ~0.18 s | **~0.18 s** (no perf blowup) |

**Ranking impact (the fix changes the answer toward correctness, NOT a wrong number
to paper over):** under the broken cycle-break, `:uploading…` is absent and
`call_exec:ModuleSource.asModule` save@0 = 209 ms; under Approach 2 `:uploading…`
surfaces at **688 ms** and `ModuleSource.asModule` rises to **376 ms** — the cycle-
break had been under-crediting classes whose finish the broken cycle swallowed.

---

## 6. What the owner must decide

1. **Adopt this fundamental fix in the shared `wcanalyze` replay** (fixes native +
   OTel + PR #13393 in one place). It is the cleanest validated option; `startOf` is
   off the table (wrong), naive emit re-root explodes fallback anchors (3474) and is
   worse.
2. **`FallbackAnchors` §6.1 signal → 0** under the fix (prefix-spawn always anchors;
   the recorded-offset path is now only the rare in-flight-parent fallback). Decide:
   retire the signal, or have `spawnTo` increment a refined counter only when it hits
   the in-flight/self-ref fallback. (`gate.go` + `TestGateFallbackAnchorsReportOnly…`
   need updating either way.)
3. **Confirm the `≤ startNS[target]` recorded-back-edge predicate** is the form you
   want (it uses `endNS[a.ref]` as the join's recorded wait-end proxy; for a JOIN
   wait waitEnd ≈ targetEnd, so this is exact-enough — but the real wait-end could be
   threaded through the compiled action if you prefer it exact).
4. **Perf note:** `spawnTo` re-walks a parent's prefix per out-of-order reference;
   measured negligible (0.18 s on 11k ops) but worth a memo if multi-million-op
   traces stress it.

Throwaway experiment lives in the worktree (env-gated `WCPROF_APPROACH2`, the two
fixtures, the cycle extractor `cmd/wcprof-cyc2`); reverted to a clean tree after this
writeup. Artifacts: `/tmp/otel-exec.jsonl`, `/tmp/native-exec.dump`.
