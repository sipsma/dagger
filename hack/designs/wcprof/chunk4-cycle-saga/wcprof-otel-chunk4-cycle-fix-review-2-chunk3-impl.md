# wcprof × OTel — cycle fix Round-3 review (by the Chunk 3 implementer)

**Analysis only — no code, no commits.** Reviewing the implementer's
`spawnTo` (prefix-spawn + the concurrent-wait skip-predicate) against my worktree's
actual `engine/wcprof/wcanalyze/replay.go` (HEAD `107ebe5c0c`, the pre-fix version:
`finish` at `:308`, the over-reach `finish(par)` at `:319`, the recorded-offset
fallback at `:329-333`, the universal `inFlight` cycle-break at `:341-345`, the
in-order `actSpawn`/`actWaitJoin` at `:377-382`) and the proposed `spawnTo` in
`wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md` §4. I cannot re-run the trace;
where a claim needs the trace/native suite I say so.

The fix is **right in its core** (scaled prefix-to-spawn + recorded-ordering skip),
and I now confirm prefix-spawn does NOT cycle for the class I proved acyclic in
round-3 — but my round-3 acyclicity argument covered the implicit-JOIN path only,
and re-examining the explicit-WAIT path surfaces exactly the residual the implementer
hit. My verdicts below; two of them carry caveats that need closing before this lands
in validated native code.

---

## My charge — does pre-emptive skipping PRESERVE "CycleWarnings = genuine signal"?

**The skip-predicate PRESERVES it. A separate part of `spawnTo` — the recorded-offset
fallbacks — partially undermines it and must be made to count.**

### Why the skip preserves it (re-examining the explicit-WAIT path my round-3 missed)

My round-3 step-4 proved the implicit-JOIN path acyclic: a child joined before the
target's spawn ended before the target existed, so it can't reach the in-flight
stack. **That argument did not cover the parent's own pre-spawn explicit WAIT** —
which is the residual: `spawnTo(op#250, op#251)` replays op#250's prefix, and op#250
*itself* has a wait on `D` whose `at` precedes op#251's spawn, so the naive prefix
processes it → `finish(D)` → re-enters the in-flight ancestor → loop (doc §3, RING).

The skip-predicate (`spawnTo`: process a pre-spawn `actWaitJoin` only if
`endNS[a.ref] <= startNS[target]`, else skip) closes this **correctly and without
hiding genuine cycles**, for one decisive reason: **the skip is PREFIX-ONLY.** It
lives in `spawnTo`, which only computes a *child's spawn-start*. The same wait is
still processed, unskipped, in op#250's **own** full `finish` (`replay.go:379`,
`actWaitJoin → finish(a.ref)`), where op#250's clock legitimately advances past `D`.
So:

- A wait that is **concurrent with the spawn** (`endNS[D] > startNS[op#251]` ⟹ op#251
  spawned before `D` finished ⟹ `D` did not gate it) is skipped *only* for the spawn
  decision. Serializing op#251 behind `D` would be a **spurious** over-serialization
  (a false dependency) — the same *class* of artifact as the original anchor
  over-reach. The would-be cycle it forms is spurious, so suppressing it is correct,
  not hiding.
- A **genuine** cycle through that wait (D depends back on op#250, gating) is **not**
  removed: op#250's full `finish` still processes the wait and re-enters op#250
  in-flight → `CycleWarnings++` at `replay.go:341-345`. The prefix skip cannot mask
  it because the prefix is not where op#250's own finish is computed.

So the skip removes **only spurious prefix over-serializations** while genuine cycles
keep surfacing in `finish`. That is exactly the property: post-fix, a `CycleWarning`
means a genuine recorded cycle. The pre-emptive-skip-vs-reactive-break distinction
*helps* here — pre-emptively skipping a provably-non-gating wait yields the **correct**
schedule (no false serialization), whereas letting it form and reactively breaking it
(the old path) yields a crude recorded-duration approximation **plus** a spurious
warning. Pre-emptive is strictly better for the concurrent-wait class.

### The caveat that DOES dent the property — `spawnTo`'s recorded-offset fallbacks

`spawnTo` (doc §4) keeps two recorded-offset fallbacks: when the parent's parent is
**in-flight** (lines 178-184) and when the parent **itself** is in-flight (186-188).
Both `setStart(...)` at `simStart[par] + recorded-offset` and **return without
incrementing any cycle/anchor counter**. These fire when a cross-tree reference
reaches an op whose ancestor is mid-replay — and a *genuine* cycle that closes via a
spawn-anchor re-entry into an in-flight ancestor would be **silently broken here**,
not at `finish:341`, so it would **not** be counted. That is a (rare) hole in
"CycleWarnings counts every genuine cycle."

**Recommendation:** the `spawnTo` in-flight fallbacks must increment the
cycle/residual counter (or a companion `OutOfOrderAnchors`/`AnchorFallbacks` metric),
so no loop is silently dissolved. This is the generalization of the cycle-guard I
asked for in round-3, applied to `spawnTo`'s own recursion. With it, the property is
fully restored: every genuine cycle is counted, whether broken in `finish` or
`spawnTo`. (This is also the same counter that replaces the retired `FallbackAnchors`
§6.1 signal — see blockers.)

**Net:** PRESERVED by the skip; needs the fallback-counting fix to be airtight.

---

## Q1 — Emit vs replay: legitimate general replay fix, or papering over an OTel emit bug?

**The §3.1 attribution is faithful; the residual is a general REPLAY
over-serialization; the fix belongs in the replay; it is NOT a forbidden seam.**

The residual surfaces because OTel §3.1 attaches a *suppressed* caller's wait to the
ancestor's `call_exec` span (the ancestor "is the op that actually blocked"), so
op#250's `call_exec` carries a wait on `D` that a *concurrent suppressed sibling*
incurred. Native records a separate `call` op per caller, so the wait lands there and
op#250 never carries it — hence native 5→0 but OTel still 5.

- **§3.1 is faithful.** The blocked time *did* occur under op#250's `call_exec`
  subtree (the suppressed caller is op#250's sub-work). Attributing it to op#250's
  subtree is correct; what was wrong was the *replay* reading "a wait somewhere under
  op#250" as "op#250 synchronously gated op#251's spawn on it." dagql fans selections
  out concurrently, so an op legitimately has a wait that overlaps its own concurrent
  spawn — that is real, faithful structure, not an emit bug.
- **The fix is general and belongs in the replay.** "An op has a wait concurrent with
  a child spawn" is a general pattern the replay must handle regardless of source.
  Native dodges *this instance* only because its finer op granularity puts the wait on
  a different op — but native can hit the same pattern whenever one op genuinely has a
  concurrent wait + spawn. So the gating predicate is a correct general replay rule,
  not an OTel-specific patch. The implementer's "the over-serialization is general; the
  OTel-specific part is only *where the wait is attributed*" is right.
- **Not a seam.** A forbidden seam would be suppressing or reclassifying a *faithful
  wait edge* to dodge the cycle. This does the opposite: it keeps every wait, and only
  decides — by recorded ordering — whether a wait **gated a spawn**. That is a
  modelling rule, not a data edit.

So: fix in the replay (done), §3.1 attribution stays, no emit change required. (One
forward note: §3.1's choice does load more "concurrent-wait-on-call_exec" pressure
onto the replay than native's granularity does; that's acceptable *given* the replay
now handles it correctly — but it is the reason the residual is OTel-first, and worth
a sentence in §3.1's writeup so it's not rediscovered as a surprise.)

---

## Q2 — Skip-predicate soundness: is `endNS[target] ≤ startNS[spawn]` the right gating test?

**Sound in principle — it never skips a genuine gating dependency — with two
real misclassification risks at the boundary that must be nailed down.**

- **It cannot skip a genuine gate.** If op#250's wait on `D` genuinely gated op#251's
  spawn (op#250 waited for `D`, *then* spawned op#251), then by definition op#251
  spawned after `D` finished: `startNS[op#251] ≥ endNS[D]`, so `endNS[D] ≤
  startNS[op#251]` → **processed**, never skipped. The only waits skipped are those
  with `endNS[D] > startNS[op#251]` — i.e. op#251 spawned *before* `D` finished, so
  `D` provably did not gate it. So no under-serialization of a real dependency, hence
  no over-crediting of a speedup. ✔
- **Sound under counterfactuals.** The predicate keys on **recorded** times, which is
  exactly how the whole replay determines structure ("join every child that had
  originally ended by t", `replay.go:23-27`): the recorded ordering fixes the gating
  *structure*, then factors scale *durations*. A counterfactual does not change
  whether the recorded run spawned op#251 before `D` finished. So keying the gate on
  recorded order is consistent with the model, not a new unsoundness. ✔
- **Misclassification risk 1 — the wait-end proxy + ε.** The predicate uses
  `endNS[a.ref]` (the wait *target's* recorded end) as a proxy for the wait's own end.
  The doc admits this ("for a JOIN waitEnd ≈ targetEnd, exact-enough"). But the
  `actWaitJoin` classification itself uses a 1 ms ε (`replay.go:165`,
  `w.EndNS ≥ w.Target.EndNS − joinEpsilonNS`), and the skip uses a bare `≤` with no ε.
  A wait whose true end is within ε of the target end, near a spawn boundary, can be
  classified inconsistently between the two predicates. **Recommend threading the
  wait's actual recorded `EndNS` into the compiled action and using it (with a
  consistent ε policy) rather than the target-end proxy** — the residual RING itself
  involved a **zero-duration** wait (`op#251 [190,190]`), exactly where boundary
  precision matters most.
- **Misclassification risk 2 — cross-client clock skew.** `endNS` and `startNS` of a
  wait-target vs a spawned child can come from *different clients' clocks* (nested
  clients, module runtimes — the design §9 epoch/skew seam). Skew could make a genuine
  gating wait look concurrent (`endNS[D] > startNS[op#251]` by skew) → wrongly skipped
  → under-serialize → over-credit. This is **not new** (the existing implicit join
  already compares cross-client recorded times), so the predicate inherits the model's
  existing skew exposure rather than adding a worse one — but under Erik's accuracy bar
  it is worth stating that the predicate's correctness rests on the same
  recorded-time/no-skew assumption the model already makes, and the module workload
  (heavy nested-client module loading) is precisely where skew is most plausible.

So: the gate test is the correct one; pin down the wait-end-proxy/ε boundary and note
the (pre-existing) skew dependence.

---

## Q3 — Residual taxonomy: skip-concurrent vs break-genuine — correct + complete?

**The two-tier policy is correct for the wait path; it is incomplete by one case (the
`spawnTo` recorded-offset fallback), which is the same hole as my charge's caveat.**

- **Tier 1 (skip, pre-emptive):** a pre-spawn wait that outlasted the target's spawn
  is concurrent/non-gating → skip in the prefix. Correct, and *prefix-only* so it does
  not touch the op's own finish.
- **Tier 2 (break + count, reactive):** a genuine cycle → `inFlight` break at
  `finish:341` + `CycleWarnings`. Still reached, because the skipped wait is processed
  unskipped in the op's full finish.

These two compose cleanly: skip = "this wait does not gate *this spawn*"; break =
"this op's *own* finish re-entered itself" — different questions, no overlap, and a
genuine cycle always reaches Tier 2 via the full finish. Pre-emptive-vs-reactive
matters and is the *right* split: the concurrent wait is *provably* non-gating, so
pre-emptively excluding it is exact; genuine cycles are *not* provably anything, so
reactively breaking + flagging them is right.

**The missing third case:** `spawnTo`'s in-flight-ancestor / self-ref fallbacks
(doc §4 lines 178-188) are *neither* a clean skip *nor* a counted break — they
silently emit a **recorded-offset** anchor. That is simultaneously (a) the
CycleWarnings hole (Q-charge caveat) and (b) a residual of the very `startOf`
approximation Erik rejected, now confined to the in-flight-ancestor corner. So the
taxonomy needs a Tier 3: **in-flight-ancestor anchor → recorded-offset last resort,
COUNTED, and validated as rare + counterfactually immaterial** (see Q5). Completing
that closes both the signal hole and the accuracy hole.

---

## Q4 — First-write-wins: correct + robust, or fragile?

**Correct for the scope that matters (the cross-referenced/cycle ops), but it rests on
a replay-order assumption + a pre-existing in-order over-serialization that should be
made consistent.**

The mechanism: `spawnTo` skips the concurrent wait → sets op#251's start at the
gating-only clock (correct, ~190). The full `finish(op#250)` later processes that wait
(`replay.go:379`, no skip) and would, at its `actSpawn(op#251)` (`:378`), anchor
op#251 at the post-wait clock (~205) — but op#251 is already started, so `setStart`
is a no-op (`:299-304`, first-write-wins) and the correct prefix value survives.

- **Why it's correct here:** for the cross-referenced ops, op#251 is referenced
  *out-of-order* (via A's wait) *before* op#250's in-order finish, so `spawnTo` writes
  first. And the two computations are *different quantities* — op#251's spawn-start
  (gating-only) vs op#250's own finish (includes the concurrent wait) — so they do not
  truly "disagree"; first-write-wins keeps the spawn-start that `spawnTo` alone
  computes correctly.
- **The fragility:** the skip is **not** applied in the in-order `finish` path. If
  op#250's full finish runs *before* op#251 is referenced out-of-order, then
  `finish`'s `actSpawn(op#251)` writes op#251's start at the **concurrent-wait-advanced
  clock** (~205) → op#251 over-serialized, and the later `spawnTo` is a no-op. This is
  **pre-existing** (the current `finish` already advances past a concurrent pre-spawn
  wait before `actSpawn`), so the fix does not *regress* it — but it means op#251's
  start is **replay-order-dependent**, and the favorable order (spawnTo-first) is what
  the measured module workload happens to exhibit, not a structural guarantee.
- **Recommendation:** apply the gating principle **consistently** — i.e., when
  *anchoring a child* in the in-order `finish` path too (`actSpawn` should anchor at a
  gating-only clock, not the running clock that has absorbed a concurrent wait), or
  prove the out-of-order-first ordering for any op that can be a concurrent-wait spawn.
  Under Erik's "accuracy is the bar," an order-dependent start is a latent wrong number
  even if today's trace lands on the right side of it.

So: robust enough that the measured results are correct, but it leans on an unproven
ordering + a pre-existing in-order over-serialization that should be closed for the
fix to be *fundamentally* order-independent.

---

## Q5 — Remaining blockers

1. **Count the `spawnTo` recorded-offset fallbacks (the renamed FallbackAnchors
   signal).** Doc §6.2 already flags `FallbackAnchors → 0`; the right resolution is
   *not* to retire the signal but to have `spawnTo`'s in-flight-ancestor/self-ref
   fallbacks increment a counter — this both preserves the §6.1 diagnostic *and*
   closes my CycleWarnings hole. Update `TestGateFallbackAnchorsReportOnlyAndThreshold`
   to the new semantics.
2. **Validate the in-flight-ancestor recorded-offset corner.** The fix **confines**
   `startOf`'s approximation rather than eliminating it (doc §6.2: "the recorded-offset
   path is now only the rare in-flight-parent fallback"). Under Erik's bar this corner
   must be shown to be a *true* last resort (no scaled answer computable while an
   ancestor is mid-replay) **and** counterfactually immaterial (or fixed). Needs a
   targeted fixture: a counterfactual that scales an in-flight-ancestor's pre-spawn
   path and confirms the corner doesn't reproduce the config-parse-style miss.
3. **Wait-end proxy + ε boundary** (Q2): thread the wait's real recorded end into the
   compiled action; reconcile the bare-`≤` skip with the ε-based `actWaitJoin`
   classification; cover the zero-duration-wait boundary (the residual RING had one).
4. **In-order spawn-clock consistency** (Q4): apply the gating skip when anchoring a
   child in `finish` too, or prove spawnTo-first ordering.
5. **Native full-suite regression.** The fix lands in the **shared** replay (PR
   #13393). The doc reports the chunk2/3/4 oracle/ranking fixtures + `wcanalyze`
   counterfactual tests pass, but I'd require the **complete native wcprof analyzer
   test suite** (PR #13393's own tests, not just the OTel fixtures) to pass before
   touching validated native code — and a re-run of the §6.2 oracle on the *module*
   workload to confirm post-fix **native↔OTel rankings still agree** (the doc shows the
   ranking moves — `:uploading…` to 688 ms, `ModuleSource.asModule` to 376 ms; that the
   move is *toward* correctness is plausible since the broken cycle-break was
   under-crediting swallowed finishes, but the new numbers should be cross-checked
   against native producing the same, not just asserted).
6. **Perf** (doc §6.4): `spawnTo` re-walks a parent prefix per out-of-order reference;
   0.18 s on 11k ops is fine, but the design targets multi-million-op traces — memoize
   the prefix clock if a large trace stresses it (the re-walk is O(out-of-order-refs ×
   prefix-length)).
7. **Root scheduling:** verify `spawnTo`'s `par < 0` recorded-start branch (doc §4
   line 175) and its reads of `simStart[root]` don't conflict with `Run`'s root
   chaining (`replay.go:275-295`) — roots are started by `Run` first, so `spawnTo`
   should read those values, but confirm the par<0 branch is genuinely unreachable for
   chained roots.
8. **(Carried) service.start §3.4 self-erasure re-root** — independent of this replay
   fix, still owed in both sources, still needs a "slow start headlines" assertion.

---

## Summary

- **Does pre-emptive skipping preserve "CycleWarnings = genuine signal"? PRESERVED by
  the skip** — it is prefix-only, so it removes only *spurious* concurrent-wait
  over-serializations while the same wait still surfaces a *genuine* cycle in the op's
  full `finish` (`replay.go:341/379`). My round-3 acyclicity argument covered the
  implicit-JOIN path; the explicit-WAIT residual is correctly resolved by the
  recorded-ordering skip. **Caveat:** `spawnTo`'s in-flight-ancestor recorded-offset
  fallbacks silently break loops without counting — make them increment the
  cycle/anchor counter to keep the signal airtight.
- **Emit vs replay:** §3.1's suppressed-caller-wait-on-ancestor attribution is
  **faithful**; the residual is a **general replay over-serialization**; the fix
  belongs in the shared replay and is **not** a forbidden seam (it keeps every wait,
  only decides gating by recorded order). Native dodges this instance via finer ops,
  but the rule is general.
- **Skip-predicate soundness:** `endNS[target] ≤ startNS[spawn]` is the **correct**
  gate — a genuine gating wait always satisfies it (so is never skipped); sound under
  counterfactuals (recorded-order structure + scaled durations, per the model).
  **Misclassification cases to close:** the wait-end *proxy* + missing ε (esp. the
  zero-duration wait in the real residual), and cross-client clock skew (pre-existing
  §9 exposure, but the module workload is where it bites).
- **Remaining blockers:** count + validate the in-flight-ancestor recorded-offset
  corner (it *confines* rather than eliminates `startOf`'s approximation); fix the
  wait-end/ε boundary; make the in-order `actSpawn` spawn-clock gating-consistent (Q4
  order-dependence); pass the **full native** wcprof suite + re-run the §6.2 oracle on
  the module workload to confirm native↔OTel agree post-fix; perf memo for huge
  traces; verify root-chaining interaction; (carried) service.start re-root.

**Bottom line:** Erik's adopt-prefix-spawn decision is sound and the skip-predicate is
the correct, recorded-ordering-grounded resolution of the residual — it preserves my
CycleWarnings-as-signal property because it is prefix-only and genuine cycles still
surface in `finish`. Before it lands in validated native code, close four things: (1)
count the `spawnTo` recorded-offset fallbacks (signal + the residual `startOf`
approximation it still hides), (2) the wait-end-proxy/ε boundary, (3) the in-order
spawn-clock gating consistency, and (4) the full native-suite + module-workload oracle
re-validation. None of these reopens the decision; they harden it to the accuracy bar
Erik set.
