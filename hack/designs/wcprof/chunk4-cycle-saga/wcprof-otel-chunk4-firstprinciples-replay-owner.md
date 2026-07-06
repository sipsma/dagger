# wcprof × OTel — first-principles rational model + items 1&2 (replay owner)

**Reviewer:** replay owner — I own `Run()` (the chaining), the `spawnTo` fallback,
and the replay, so this is mine to lead. Verified against `replay.go` at
`e8c0dfe498`/`692aaabd3f`. No code, no commits.

## (c) Alignment with the governing principle — I agree, fully

**The analysis is a pure function of the recorded data; it honors the causal
structure the data records and nothing it does not; no inference, no fallback, no
compensation; when a rational model reports something odd, the bug is in the data
→ fix the emit.** I align with this without reservation. It is not a new
constraint bolted on — it is the *same* discipline the whole design already
demands of the loader ("zero causal inference," `wcprof.parent ?? parentId` and
nothing more) and of the emit ("make every nesting the analyzer reads as
synchronous truly synchronous"). The `Run()` chaining and the `spawnTo`
recorded-offset fallback are precisely the two places the *validated native
replay* (PR #13393) violated this principle — they are inference and compensation
baked into the analysis — so applying the principle means removing them. I should
have been holding the replay to this standard from the start; I was not (I twice
endorsed the fallback and once proposed `startOf`, which is the same anti-pattern),
and I own that.

**One clarification, not a pushback:** the principle must not be misread to forbid
the replay's *model assumptions*. The implicit join ("a child ending by t was
synchronously joined by t," `replay.go:24-27`) is **not** inference-compensating-
for-a-gap — it is the model honoring the recorded nesting *under the emit's
synchronous-nesting guarantee*. The distinction is exact and the principle draws it
correctly: the implicit join reads a **recorded causal edge** (parent⊃child
nesting) and interprets it per the model; the chaining reads **temporal order with
no edge** and invents a dependency. The former is honoring data; the latter is
inference. Keep the first, kill the second. (And where a nesting turns out *not* to
be synchronous, that is an emit bug the gate/cycle flags — data fix, not a model
patch. Consistent.)

The one real cost of the principle, worth stating plainly: it puts **more burden on
the emit** — every real dependency must be a recorded edge, because the analysis
will no longer guess any. That is the correct division of labor (the emit knows the
structure; the analysis must not), and it is the whole point.

## (a) Items 1 & 2 review

### Item 1 — zero-duration wait-target fix: correct, rational, properly scoped ✔

This is a genuine **model** bug (not data), and the fix is a rational-model
correction — exactly the kind of analysis change the principle *permits* (it makes
the model logically correct for the data it's given). I verified both parts:

- **`joinUpTo` defer** (`advance`): when it reaches an unstarted child `c` with
  `startNS[c] == t` (the gate's instant), it now **defers** instead of anchoring
  `c` at the pre-gate clock. The only way an in-range child is unstarted at its
  *own* end is a zero-duration child whose spawn and end both equal `t`; deferring
  lets the rank-0 max-gate raise the clock, then the rank-1 spawn anchors `c` at the
  **gated** clock. Correct: a zero-dur wait-target's finish was being computed
  pre-gate and propagating (the 250-vs-300 case). ✔
- **`actionRank` spawn-before-self** (gate 0 < spawn 1 < self 2 < markers 3): a
  deferred zero-dur child's spawn now anchors it **concurrent** with a same-instant
  self-segment, not serialized after it. Scoped check holds: a *normal* child's
  non-empty interval is carved out of the parent's self (`SelfSegments` subtracts
  it), so a self-segment can never *start* at a normal child's spawn instant — only
  a zero-dur child can share that instant. So the rank flip touches nothing but
  zero-dur children, and real traces are bit-unchanged. ✔
- It also cleans the `SimStartConflicts` signal (the benign zero-dur source is
  gone), so a conflict now genuinely implies a fallback. Good.

**One residual to note (low, theoretical):** the defer `return`s out of `joinUpTo`,
so a *second* child `d` ending exactly at `t` with `ID > c.ID` (sorted after the
deferred `c`) is also deferred to the next `joinUpTo`. For `d` already started
(`endNS[d]==t`, `startNS[d]<t`) this is benign (its join is a `max`, commutative;
its anchor is untouched). The only sharp edge is two *zero-dur* children co-ending
at `t` where one is a wait target whose finish should reflect the other — vanishing
in practice and absent on all traces, but worth one targeted test so it's not a
future surprise. Not a blocker.

### Item 2 — `FallbackAnchors` hard-fail: a legitimate *interim guard*, but a band-aid over the anti-pattern; item 3 is the real fix

Under the principle, the honest answer is **both**:
- As a **gate**, hard-failing "the analysis used a recorded-offset fallback" is
  *aligned* with the principle in spirit — it refuses to ship a report produced by
  the forbidden compensation. It's free now (0 on every trace) and a reasonable bar
  while the fallback still exists.
- But it is treating a **symptom**. The principle says the fallback **should not
  exist**. Hard-failing on "the analysis compensated" is a band-aid; the
  first-principles fix is to **remove the compensation** so there is nothing to fail
  on — which is item 3 (anchor roots from first principles). The commit message
  itself concedes this ("the cross-root shape that still produces fallbacks is
  item-3 territory").

So: **keep the hard-fail as an interim assertion, but it is not the fix.** After
item 3, the *cross-root* fallback is gone by construction (`FallbackAnchors == 0`
always), and the hard-fail becomes vestigial for that case — at which point it
should be **re-pointed** to its only legitimate remaining target: the
**in-flight-ancestor** case, which is not an "approximation we couldn't avoid" but
**unfaithful data** (a non-synchronous nesting — see below). So the gate survives,
but reframed from "you used an approximation" to "the emit produced an impossible
structure." Don't leave it as the recorded-offset band-aid.

## (b) Item 3 — the rational model

### The model, from first principles

> **A root has no incoming causal edge. The data therefore says it is independent.
> Anchor it at its own recorded start, and replay it honoring only the recorded
> edges (parent⊃child spawns, wait edges, implicit joins). No chaining inference.
> No recorded-offset fallback. Anchor *all* roots before replaying any, so a
> cross-root edge always resolves to an already-anchored target.**

Two consequences, both from the data alone:
- A **cross-root wait edge is positive evidence the two roots are concurrent** (a
  wait can only block on work that overlaps it in time). So the chaining model —
  which assumes successive roots are *sequential* — is **provably wrong for exactly
  the roots that have cross-root edges.** The `SimStartConflict` it reports there is
  the chaining heuristic fighting the correct independent anchor; it is an artifact
  of the analysis's own compensation, not a real signal.
- With roots pre-anchored at their recorded starts (an exact fact, not a guess), a
  cross-root reference finds its target started, so **`spawnTo`'s `par<0` fallback
  never fires** — `FallbackAnchors` and `SimStartConflicts` are **0 by
  construction**, not by luck.

### The three test cases — what the data records, and the correct answer

**(a) Concurrent cross-root dedup → makespan 200.** Data records: R_A, R_B both
start t=0 (two roots, no incoming edge ⇒ independent); R_B spawns T at 100, T runs
100→300; R_A's W carries a **recorded wait edge to R_B's T**. Rational replay:
anchor R_A, R_B at 0; honor the wait edge → W blocks on `finish(T)`. Baseline
makespan = 300. What-if (R_B setup → 0): R_B reaches T's spawn at 0 (the prefix walk
carries the factor), T runs 0→200, W unblocks at 200 → **makespan 200, a 100ms
saving propagating across the root boundary through the recorded wait.** Zero
fallbacks, zero conflicts. **The data answers it exactly; no chaining, no fallback
needed.** ✔

**(b) Sequential CLI — the chaining IS compensating.** Data records: R_A [0..T1],
R_B [T1+gap..]. The shell ran R_B after R_A returned **outside the engine** — there
is **no recorded engine edge R_A→R_B** (R_B is a fresh session/query with no causal
link to R_A's result). So the data says **independent**. Rational replay anchors
both at recorded starts; scaling R_A's class shrinks R_A but **does not move R_B**
(its start is a recorded fact) → makespan = R_B's end, unchanged → R_A saves 0. **Is
that right? Yes — for the engine makespan, which is what we analyze.** The intuition
"R_A faster ⇒ total wall-time shrinks because R_B runs earlier" is the **shell's**
wall-clock, and the shell's serialization is **not an engine dependency** — the
engine cannot and should not record it. The chaining was **inferring** that
dependency from temporal order: the anti-pattern. **Remove it.** If a *real*
cross-query data dependency ever exists (R_B literally consumes R_A's result), that
is a recorded edge to emit — but that is unusual and is the exception that proves
the rule. (Reinforced by design §10 decision 2: one trace = one session; cross-
session wall-time is out of the OTel source's scope entirely.)

**(c) Sub-session — the launch IS a recorded edge.** R_B launched by R_A mid-flight.
For a nested client (§2.6) R_A propagates its exec span's **traceparent** into the
container, so R_B's spans nest under R_A's exec span — i.e. **R_B is not a root**; it
is a recorded child of R_A's exec, anchored through that edge by the ordinary
parent⊃child mechanism (R_A's implicit join waits for it). No ambiguity, no
fallback. **If** a launch path ever fails to propagate the traceparent, R_B would
appear as an independent root that doesn't track R_A's launch point — and the fix is
**in the emit** (propagate the launch edge / traceparent), never a fallback in the
analysis. So: recorded edge today; a data fix if a gap ever appears. ✔

### The analysis/model code changes (concrete)

1. **`Run()` — replace chaining with independent, up-front anchoring:**
   ```
   for _, r := range roots { setStart(r, startNS[r]) }   // anchor ALL roots first, at their recorded starts
   for _, r := range roots {
       f := finish(r); lastFinish = max(lastFinish, f)
       firstStart = min-nonneg(firstStart, simStart[r])
   }
   return lastFinish - firstStart
   ```
   Delete `chainOrigEnd`/`chainSimEnd` and both shift branches. The two-pass shape
   is required: pre-anchoring every root is what makes a cross-root forward edge
   resolve to a started target (killing the `par<0` fallback). **Baseline is
   bit-identical** (at factor 1 the chaining produced exactly the recorded starts),
   so native baseline makespan is unchanged; only multi-root *what-ifs* change — and
   they change *toward* correctness (independent roots no longer falsely shift).
2. **`spawnTo` — remove every recorded-offset fallback:**
   - `par < 0` branch: **delete** (dead once roots are pre-anchored).
   - `par` in-flight, and "prefix never reached target": these are **not**
     approximations to paper over — they are **unfaithful data** (a parent whose
     pre-spawn work references a child it has not yet spawned is temporally
     impossible in a synchronous nesting; "target isn't actually par's recorded
     child" likewise). Replace the recorded-offset anchor with an **unfaithful-data
     flag** (a counted, gate-failing signal distinct from the rational
     `CycleWarnings`) so the *emit* is fixed. Do **not** silently anchor.
3. **Genuine cycle (`finish` sees `inFlight[i]`):** keep the break, but understand
   it as flagging **unfaithful data** too — a real mutual dependency cannot occur in
   a completed (rc=0) run, so a recorded cycle is a false (non-synchronous) edge.
   `CycleWarnings > 0` ⇒ emit bug, by the same logic.

Net: the analysis becomes a pure data-honoring function. `FallbackAnchors`
disappears as a runtime path; what remains are **two faithfulness flags**
(unfaithful-inversion, cycle), both pointing at the emit.

### The emit (data) fixes for the data-insufficient cases

The principle's discipline: where the data can't answer, name the **edge the emit
must record** — never an analysis fallback.

- **Sequential independent queries (b):** *no emit fix and no edge* — they are
  genuinely independent in the engine; the shell's external serialization is **out
  of engine scope**. The "fix" is to stop inferring it (remove chaining). (Only if a
  real R_A→R_B data dependency exists would the emit record a wait edge — rare.)
- **Sub-session launch (c):** the **traceparent/launch edge** (§2.6) — already
  emitted for nested clients; the data fix is to ensure *every* launch path
  propagates it, so no sub-session is ever mis-seen as an independent root.
- **In-flight-ancestor / non-synchronous nesting:** the emit must guarantee
  **synchronous nesting** — a parent spawns a child before anything references it
  (the design's core thesis, §1.1). A trip here is an emit choke-point that re-points
  or detaches work without a wait edge; fix it at the choke point (the same class as
  the lazy re-point §3.2 and the singleflight §3.1 already handled).

## Summary

- **Items 1 & 2 verdict:** **Item 1** (zero-dur defer + spawn-before-self
  `actionRank`) is a correct, properly-scoped **rational-model** fix — native
  bit-unchanged, one low/theoretical co-terminating-zero-dur residual worth a test.
  **Item 2** (`FallbackAnchors` hard-fail) is a **legitimate interim guard but a
  band-aid over the anti-pattern** — the first-principles fix is to remove the
  fallback (item 3), after which the hard-fail is vestigial for cross-root and
  should be re-pointed to flag the in-flight-ancestor as *unfaithful data*.
- **Rational root/cross-root model:** a root has no incoming edge ⇒ independent ⇒
  anchor at its own recorded start; pre-anchor all roots; honor recorded edges only;
  no chaining, no fallback. Cross-root wait = proof of concurrency, so chaining is
  provably wrong for those roots; `FallbackAnchors`/`SimStartConflicts` become 0 by
  construction.
- **Three cases:** (a) recorded wait edge → 200, saving propagates, no fallback; (b)
  **no** recorded edge — chaining was compensating for the shell's external
  serialization; remove it, queries are independent in engine scope; (c) launch **is**
  a recorded traceparent edge → nested child, not a root.
- **Analysis changes:** `Run()` → pre-anchor all roots at recorded starts +
  independent finish (delete chaining); `spawnTo` → delete the `par<0` fallback,
  convert the in-flight/prefix-miss fallbacks into unfaithful-data gate flags; keep
  the cycle-break as a faithfulness flag.
- **Emit fixes:** (b) none — out of engine scope (stop inferring); (c) ensure
  traceparent/launch-edge propagation on every sub-session path; (inversion) ensure
  synchronous nesting at the choke point.
- **Principle alignment:** **agreed, fully** — with the one clarification that the
  implicit-join *model* (honoring recorded nesting under emit-faithfulness) is not
  the forbidden inference; the chaining (inferring from temporal order) is. The cost
  — a heavier emit-must-record-every-edge burden — is the correct division of labor.
