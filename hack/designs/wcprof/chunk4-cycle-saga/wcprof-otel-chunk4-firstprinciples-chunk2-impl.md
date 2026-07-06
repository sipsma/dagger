# Chunk 4 — items 1 & 2 + item 3 from FIRST PRINCIPLES (by the Chunk 2 implementer)

Verified against the patch + `replay.go` (`Run`, `spawnTo`, `advance`) in my worktree.
Analysis only; no code, no commits.

## Alignment with the governing principle — yes, strongly, with two refinements

I agree with it without reservation: **the analysis is a rational function of the
data and must never compensate for the data's gaps.** Chaining heuristics,
recorded-offset fallbacks, best-effort anchors — every one is the engine *guessing*
where the data is silent, and that guessing is exactly what has produced the
multi-round mess on this workstream (a wrong number that *looks* plausible is worse
than a loud "the data doesn't say"). Debug model and data separately; an odd result
from a rational model means the **data** is wrong → fix the **emit**. I've been on the
wrong side of this myself (below), so I hold it as the lesson of this whole arc.

Two refinements I'd fold in, not pushback so much as completing it:

1. **"Fix the emit" has a sibling: "out of engine scope."** When the data is silent
   because the engine *cannot* observe the fact (a shell serializing two CLI
   invocations the engine never sees), the honest outcome is not "emit the edge" — it
   is "this is outside the engine's data; the engine analyzes per-session, full stop."
   The handoff already allows this ("or it is out of engine scope — say which"); I'm
   just making it a first-class third outcome alongside "fix the emit," so nobody
   reaches for a fallback to cover an unobservable fact.
2. **The implicit join *is* the model's one inference — and that's fine, because it's
   the documented model, not a compensation.** "No inference" cannot mean "no implicit
   join" (that would delete the replay). It means *no ad-hoc compensation layered on
   top of the model*. And when the implicit join is wrong — a concurrently-spawned
   child recorded as a synchronous nesting (the whole cycle saga) — that is a **data**
   problem (the emit nested concurrent work), fixed in the emit (§2.2/§3.2), which is
   itself the principle. So the principle is self-consistent: the model = implicit
   join + wait propagation + prefix-anchor; everything else is data.

## Items 1 & 2

### Item 1 — the zero-duration wait-target fix: correct, and I own that I missed it

The fix is two parts, both correct and properly scoped:
- **`joinUpTo` defers a child whose own spawn is still pending at this instant**
  (patch lines 102-114): `if !started[c] { if startNS[c]==t { return } ... }`. I
  verified this fires **only** for a zero-duration child: an in-range child is
  `endNS[c] ≤ t`, and `startNS[c]==t` then forces `endNS[c] ≤ startNS[c]`, i.e.
  `endNS==startNS==t`. A normal in-range child was already started by its earlier
  spawn (`startNS < t`). So the deferral lets the same-instant max-gate raise the
  clock first, then the spawn anchors the child at the gated clock. ✔
- **`actionRank` spawn(1) before self(2)** (patch lines 85-96): correctly scoped,
  because only a zero-duration child can share a spawn instant with a self-segment
  start — a normal child's interval carves the self out of its spawn point
  (`SelfSegments` subtracts the child), so no normal spawn coincides with a self start.
  Hence real traces are bit-unchanged. ✔ Both faces tested
  (`TestZeroDurWaitTargetPropagation` → makespan 300 not 250;
  `TestZeroDurChildAtSelfStart` → Z=100).

**I own this gap.** In Round 6 I confirmed the implementer's "benign zero-dur
conflict" — but I reasoned only about the **leaf** case ("a zero-duration child's
finish is absorbed"). When the zero-dur child is itself a **wait target**, its
wrong-early finish *propagates* (B=50 → A=250 → makespan 250), with **no
FallbackAnchor to flag it** — a silent wrong answer, the worst kind. Fresh Codex
caught the case I didn't enumerate. Same failure mode as my fixed-wait miss: I checked
a representative case and generalized instead of enumerating. The valuable byproduct:
the fix **eliminates the benign conflict**, so `SimStartConflicts` is now a *clean*
signal — a conflict now implies a recorded-offset fallback (the test asserts
`SimStartConflicts==0`). That removes the muddiness I flagged in Round 6.

### Item 2 — the `FallbackAnchors` hard-fail: a band-aid over the anti-pattern

**Under the principle, the hard-fail is a band-aid, and it blames the data for a model
limitation.** A fallback anchor is the analysis *compensating* — the very thing the
principle forbids. Hard-failing "the analysis used a fallback" enforces on the
symptom; the principled fix is to make the fallback *not exist* (item 3). Worse, the
fallback counter conflates two categorically different things:

- **Cross-root reference (`spawnTo`, par<0):** this is **not a data problem.** The
  data is faithful — two roots with no incoming edge, plus a recorded cross-root wait.
  The "fallback" (anchor the root at its recorded start) is, as you said, *actually
  the correct independent anchor* — it is mislabeled. Hard-failing a faithful
  cross-root trace is the **data gate failing for a model limitation** (the replay
  can't yet anchor roots independently), which violates "debug model and data
  separately." This is the deeper version of my Round-6 position: I argued
  "baseline-exact, don't hard-fail"; the principle sharpens it to "the recorded-start
  anchor is *exact and correct*, not an approximation — the chaining is the illegitimate
  part."
- **In-flight ancestor (`spawnTo`, par mid-replay):** this *is* a data problem — a
  recorded inversion (a target referenced from inside its parent's prefix before the
  parent spawns it). Here failing loud is *right* — but as a **data-faithfulness
  error**, not lumped under "fallback."

**Verdict:** the hard-fail is a defensible *transitional* "fail loud, don't silently
approximate" stopgap (and it's free today — 0 on all traces), but it is **not the end
state**: item 3 removes the cross-root fallback (making it exact, not a hard-fail
target), and the in-flight-ancestor case should be reported as a distinct *data* error.
Keep it only until item 3 lands; then the cross-root branch must stop hard-failing
(faithful traces) and the inversion branch should become a data-error. The
implementer's own note ("the cross-root shape is item-3 territory") concedes exactly
this. Code is correct and backward-compatible; the *posture* is the issue.

## Item 3 — the rational model

### The model (trust the data, nothing else)

1. **A root is an op with no incoming causal edge ⇒ the data says it is independent ⇒
   anchor it at its own recorded start.** This is an exact fact, not a fallback.
2. **Honor recorded edges only.** A parent edge anchors a child through its parent's
   replayed progress at the spawn (the prefix-to-spawn `advance`); a wait edge
   propagates the counterfactual (waiter finish ≥ target finish). The implicit join is
   part of this (the documented model assumption).
3. **No chaining inference. No recorded-offset fallback.** Anchor *all* roots up front
   at their recorded starts (a pre-pass), then replay each; a cross-root wait then
   references a target whose root is already anchored, so `spawnTo` never reaches the
   par<0 branch.
4. **If an op cannot be anchored from recorded edges, that is a DATA error** (e.g. a
   recorded inversion) — fail loud, don't fabricate a start.

This is `Run`'s chaining (lines 270-294, the `chainSimEnd/chainOrigEnd` shift) deleted
and replaced by "anchor each root at `startNS[root]`," plus deleting the par<0 fallback
in `spawnTo`.

### The three test cases

**(a) Concurrent cross-root dedup — data: two roots @0 (no incoming edge) + a recorded
cross-root wait W→T.** Logically-correct counterfactual (scale R_B setup→0): both
starts fixed at their recorded 0; the saving propagates through the *recorded wait* (T
0→200 ⇒ W unblocks 200) ⇒ **makespan 200**. The rational model gives exactly this:
anchor R_A@0 and R_B@0 (exact), `advance` R_B's prefix under the factor to T's spawn
(T@0), wait W→T propagates. `FallbackAnchors=0`, `SimStartConflicts=0` **by
construction** — the recorded-start anchor is the right answer and there is no chaining
to fight it. ✔ (Today's model gets the number via the prefix-replay but *labels* the
exact root anchor a "fallback" and reports the chaining-vs-anchor conflict — both
artifacts of the anti-pattern.)

**(b) Sequential CLI (B starts after A ends).** *What does the data record?* It depends
on whether the queries are nested under the **client/CLI session span** (which is in the
trace — the loader currently treats `POST /query` as a root, §3.5). Two sub-answers:
- **If they remain independent roots:** there is **no recorded A→B edge** (the shell
  serialized them outside the engine). The rational model treats them independent ⇒
  scaling A does **not** shift B. That is the logically-correct answer *for this data*.
  The chaining model's "shift B earlier" is **inference compensating for a missing
  edge** — exactly the anti-pattern. Remove it.
- **The correct resolution is the EMIT, not the chaining:** nest the query roots under
  the recorded **client session span** (their real parent in the trace). Then sequential
  serialization is captured *as data*: the session span joins query-A (implicit join)
  before spawning query-B, so scaling A's work makes the session reach B's spawn earlier
  — the saving propagates through the *recorded parent structure*, no chaining needed.
  Concurrent queries nest concurrently; sequential ones nest sequentially; the data
  tells the truth either way.
- **If there is genuinely no client span** (an un-instrumented client): the cross-CLI
  serialization is **out of engine scope** — the engine analyzes per-session and says so;
  it does not invent a chain.

**(c) Sub-session (R_B launched by R_A mid-flight).** *Does the data record the launch
as a causal edge?* It **should**: OTel propagates the launching exec's traceparent into
the nested client (design §2.6), so R_B's session span nests under R_A's exec — R_B is
**not a pure root**, it has a recorded parent edge. Then the rational model anchors R_B
through that edge (R_B's start tracks R_A's replayed progress at the launch), and scaling
R_A's pre-launch work correctly shifts R_B. No ambiguity, no fallback. **If the launch
edge is missing** (a real DATA gap — traceparent not propagated, or a fire-and-forget
launch), the model sees an independent root and won't shift R_B; if that's wrong, **the
fix is the EMIT — record the launch edge** (the §2.6 traceparent / a wcprof
nested-client link), never a fallback.

### Model/analysis code changes (all *removals*, fittingly)

- **Delete the chaining in `Run`** (the `chainSimEnd/chainOrigEnd` shift, lines 277-294);
  pre-anchor every root at `startNS[root]`, then `finish` each.
- **Delete the par<0 fallback in `spawnTo`** — dead once roots are pre-anchored; the
  recorded-start anchor it used to compute is now Run's first-class exact anchor.
- **Turn the in-flight-ancestor "fallback" into a DATA error** (a recorded inversion is
  not something to approximate around) — surfaced distinctly from cross-root.
- The genuine-orphan `setStart(c, clock)` inside `joinUpTo` (patch line 117) is itself a
  residual compensation (a child spawned outside its parent's interval is a data
  inconsistency) — under the principle it too should be a data error, not a best-effort
  anchor. Minor/rare, but name it.
- Makespan for independent roots becomes `max(finish) − min(start)` over the
  independently-anchored roots — gaps preserved as real idle, not chained away.

### EMIT (data) fixes for the data-insufficient cases

- **(b):** nest query roots under the recorded **client/CLI session span** so
  cross-query serialization is captured as parent/implicit-join structure (loader/emit),
  removing the need for the chaining inference. Where no client span exists → out of
  engine scope (no fix; document it).
- **(c):** ensure the **nested-client launch edge** is recorded (§2.6 traceparent /
  wcprof link) so a sub-session is anchored through it, not as an independent root.
- **in-flight ancestor:** the inverted reference is an emit ordering bug — record the
  causal order correctly (or it indicates a genuine concurrency the emit must express as
  a non-nesting edge, cf. §3.2's `wcprof.parent`).

## Summary

- **Items 1 & 2 verdict:** Item 1 (zero-dur defer + spawn-before-self rank) is correct,
  scoped to zero-dur only, native bit-unchanged, and fixes a *silent* wrong answer I
  missed in Round 6 (I checked the leaf, not the wait-target propagation) — and it makes
  `SimStartConflicts` clean. Item 2's `FallbackAnchors` hard-fail is **a band-aid** under
  the principle: it blames the *data gate* for a *model* limitation and false-positives
  faithful cross-root traces; keep it only as a transitional fail-loud, and let item 3
  retire the cross-root branch (the in-flight-ancestor branch should become a distinct
  data error).
- **Rational model + the three cases:** anchor every root at its recorded start
  (independent, exact), honor recorded edges, no chaining, no fallback. (a) → makespan
  200 with 0 fallbacks/conflicts by construction; (b) → independent unless nested under
  the client span (the EMIT fix), chaining is compensation to delete; (c) → anchor
  through the recorded §2.6 launch edge, and if it's missing the fix is the EMIT.
- **EMIT fixes:** nest queries under the client session span (b); record the
  nested-client launch edge (c); fix the inverted-reference ordering (in-flight ancestor).
- **Principle:** aligned, with two completions — "out of engine scope" is a valid third
  outcome beside "fix the emit," and the implicit join is the model's documented
  assumption (not a forbidden inference; when it's wrong, that too is a data fix).
