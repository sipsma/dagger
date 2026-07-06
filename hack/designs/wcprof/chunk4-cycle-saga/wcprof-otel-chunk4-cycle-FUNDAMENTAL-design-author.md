# Chunk 4 cycle — is original-frame anchoring a principle or a bug? (design author)

Analysis only — no code, no design edits. I am the design author; in round 2 I
endorsed Approach 1 (`startOf`, recorded-offset anchor) partly on the strength of
the replay's "cross-tree wait targets are anchored at original times" language
(`replay.go:31-34`). Erik's ruling: Approach 1 gives a **wrong counterfactual**, so
it is off the table, and if the design's language means we knowingly emit a wrong
answer, **that language is the bug**. My charge: go back to *why* that language
exists, test it against the concrete example, and if it fails, say what the design
must say instead — then reason about the fundamental fix.

## Verdict in one line

**Original-frame anchoring is a BUG (a fallback approximation I wrongly elevated to
a principle), not a correct modeling choice. The replay's *real* model is
counterfactual-frame anchoring — it already does exactly that for in-order children
— and cross-tree out-of-order targets MUST get the same treatment. I retract my
round-2 endorsement of Approach 1.**

---

## 1. The principle, stated at its strongest — then killed

**Strongest version of the principle.** *"A counterfactual scales class self-times.
An op's START is a structural fact (when it was spawned in the recorded run); to
keep the whole simulation in one coherent time frame, hold every op at its recorded
start and let only durations change. Re-deriving starts from scaled upstream work
'mixes frames' and corrupts the schedule — so an op reached out-of-order (before its
own root replayed) is pinned to its original time."*

That is the most charitable reading of `replay.go:31-34`. It does not survive two
independent tests.

### Test A — the concrete config-parse example: it gives the wrong answer

`POST/query` spawns `app`,`lib`. `app`: 100 ms config-parse self, then spawns
`T=load foo` (T: 100→300). `lib`'s `W` dedups onto `T` ⇒ `W` waits on `T`
(cross-tree). What-if: config-parse → 0. **True answer:** T spawns at 0, runs
0→200, W unblocks at 200 — config-parse is on W's critical path *via the cross-tree
wait*, so a correct profiler MUST credit it 100 ms.

- **Original-frame principle (Approach 1, `startOf`):** anchors `T` at
  `app.start + recorded_offset = 0 + 100 = 100`, *independent of the factor*. T
  stays 100→300, W stays 300, saving = **0 → wrong.** It cannot credit config-parse
  because it severs the only channel through which the scaling reaches W: T's start.
- So under the principle, "T anchored at its original time, unaffected by
  config-parse's scaling" is **plainly the wrong counterfactual.** There is no
  rigorous reading where it is right: the whole question "what if config-parse were
  faster?" is precisely "does downstream work (T, then W) start sooner?" — and the
  principle answers "no" by construction.

### Test B — the consistency kill (this is the decisive one)

The replay **already anchors in-order children in the counterfactual frame** — and
has from day one. When a parent replays and reaches a child's spawn, it sets the
child's start to the parent's *current clock*, which already includes the parent's
**scaled** self-time:

```
replay.go:375-378
case actSelf:  clock += int64(float64(a.dur) * factor)   // parent self, SCALED
case actSpawn: s.setStart(a.ref, clock)                  // child start = parent's SCALED clock
```

So in the config-parse example, if T is reached *in order* (via `app`'s natural
replay), T is anchored at `app.start + 0 = 0` — the *correct, counterfactual*
start. The replay's primary, intended behavior **is counterfactual-frame
anchoring.**

Original-frame anchoring therefore is not a principle the replay holds — it is an
**inconsistency**: it would pin *cross-tree* targets while *same-tree* children move
with the factor. A genuine "starts are structural facts" principle would pin *all*
starts (including in-order children); the replay pins none of them in the primary
path. So the original-frame behavior is exactly and only the **fallback**
(`replay.go:324-338`) — the compromise used when the parent *cannot* be replayed
(it is mid-replay / data inconsistent) — and the `:31-34` comment is documenting
that fallback's compromise, not asserting a model. Approach 1's error is to
universalize the fallback, making cross-tree targets permanently inconsistent with
in-order children. That it happens to be acyclic is convenience, not correctness.

**Conclusion:** the language is a post-hoc description of a degenerate-case
approximation, not a justified principle. It does not survive. My round-2 reasoning
mistook the fallback for the model — I was wrong, and the config-parse case + the
in-order-consistency argument both prove it.

## 2. What the design must say instead

> **Counterfactual-frame anchoring (corrected principle).** An op's simulated start
> is its producer's *counterfactual* clock at the moment it is spawned — the
> producer replayed **with the factor** up to that spawn. This holds uniformly for
> in-order children (already true) **and** for cross-tree, out-of-order wait targets
> (the bug). A cross-tree wait target therefore *feels its producer's scaling*: if
> scaling a class shortens the producer's path-to-spawn, every downstream waiter —
> including cross-tree ones — unblocks sooner, and the counterfactual credits the
> scaled class. Original-frame anchoring is **only** a last-resort fallback for a
> producer that genuinely cannot be replayed (a real data inconsistency), and such
> a fallback must be rare and **flagged**, never the normal path.

Consequences elsewhere:

- **"Reuse the validated native replay UNCHANGED" premise → amended.** The replay's
  out-of-order anchor has a genuine bug on two counts: it *over-reaches* (replays
  the parent's full finish, causing the cycle) **and**, when it falls back, it pins
  to the original frame (wrong counterfactual). Both are shared with native. Per
  Erik's "a bug is a bug even if native shares it," the fix lands in the shared
  `wcanalyze` replay. The premise becomes: "reuse the native replay, fixing the
  genuine shared bugs the OTel work surfaced (Chunk 4: out-of-order anchor
  over-reach + frame-pinning)."
- **§1.1 / §2.5 / §6.3 cycle taxonomy → corrected.** "Cycle ⇒ unfaithful emit" is
  incomplete: a cycle can come from *either* unfaithful emit *or* the replay's
  anchor over-reach on faithful data. Discriminator (the Chunk-4 diagnostic, now
  validated): **does native cycle on the same data?** Yes ⇒ replay bug; no ⇒ emit
  bug.
- **§6.1 gate.** After a correct anchor, `CycleWarnings` becomes a *clean* signal
  (fires only on genuine dependency cycles = real unfaithful emit). So the gate's
  `CycleWarnings == 0` is made *correct*, not relaxed — and `FallbackAnchors`
  becomes a rare, genuinely-meaningful "unreplayable producer" diagnostic (keep it,
  but it should be ~0 on healthy traces). No seam.
- **§6.2 oracle.** Native and OTel must continue to agree; since the fix lands in
  the shared replay, both move together — the oracle stays valid (and the
  module-loading workload should now converge, cycle-free, under the scope-matched
  oracle from the Chunk-3 reconcile).
- **§3.0.1 / anchoring text.** Any place that leaned on "original-frame anchoring"
  as intended must be reworded to the corrected principle above.

## 3. Approach 2 (prefix-to-spawn) and the fundamental core

### Approach 2 is correct on the example and removes the over-reach

Approach 2: to anchor an out-of-order target T, replay its producer **with the
factor** only **up to T's spawn action**, then stop. Config-parse: app's prefix =
`actSelf config-parse × 0 = 0`, then `actSpawn(T) ⇒ setStart(T, 0)` → T at 0 → W at
200 → **correct.** And it removes the cycle's mechanism: the cycle came from the
anchor calling `finish(parent)` — the parent's *full* finish, whose `joinUpTo` over
**all** the parent's children (and, recursively, the grandparent's join over the
*sibling* subtree) reaches the concurrent cross-referencer that waits back. The
over-reach climbs to the common ancestor and back down the *other* subtree.
Prefix-to-spawn replays each ancestor **only up to the child-on-the-path's spawn**,
which is *before* the sibling subtree is spawned/joined — so it never reaches the
cross-referencer. Correct **and** acyclic for this structure.

### Does Approach 2 still loop? Only on a genuine cycle — which can't be present here

A cross-referencer can only be reached during prefix-to-spawn if it was spawned
*before* the target on the producer's timeline **and** waits on the target — i.e. a
real ordering inversion = a real synchronous cycle = a deadlock. The workload
completed (rc=0), so the spawn-prefix + wait dependency graph of faithful data is a
**DAG**; Approach 2's recursion terminates without a cycle. If it *did* loop, that
loop would be a **genuine** data cycle (unfaithful emit), which must be caught
loudly — see the cycle-awareness below. So: artifact cycles gone, genuine cycles
still surfaced. That is the right end state, and (unlike Approach 1) with **no**
accuracy loss.

### The real implementation cost of Approach 2 (flag this honestly)

`finish(i)` today conflates **anchor** (i's start) and **replay** (i's finish).
Prefix-to-spawn partially replays the producer (up to the spawn) — leaving the
producer half-replayed. When the producer is later reached in order, its replay must
**resume** from where the prefix stopped, not restart and not be mistaken for an
in-flight cycle. Two implementable shapes:
- **Resumable producer replay:** track each op's action-loop index + clock so a
  prefix-anchor and the eventual full replay are one continued pass. Most faithful,
  but adds per-op resumable state to the hot loop.
- **Parallel `clockAtSpawn(i)` recursion:** a pure, memoized computation of "the
  producer's scaled clock at i's spawn" that does *not* commit the producer's
  in-flight/finish state (it recursively sums scaled pre-spawn self + finishes of
  pre-spawn joined children + pre-spawn waits). Avoids resumability but duplicates
  some replay logic. Either way, more invasive than Approach 1's `startOf`. Erik's
  "do not sacrifice accuracy" makes this cost the right trade.

### The fundamental core — the seed hypothesis is right; sharpen it

`finish()` is entered from three paths: **anchor** (`finish(parent)`,
`replay.go:319`), **implicit join** (`joinUpTo → finish`, `:365`), and **explicit
wait-join** (`actWaitJoin → finish`, `:380`). The cycle-break (`inFlight`,
`:341-345`) is set **after** the anchor block (`:346`), so:
- the wait-join and implicit-join re-entries happen **after** `inFlight` is set →
  they hit the clean recorded-back-edge break;
- the **anchor** re-entry happens **before** `inFlight` is set (the anchor block
  precedes `:346`) → it is *not* recognized as a cycle; it falls through to the
  fallback + a premature replay. So the anchor path both **over-reaches** (full
  parent finish, not prefix-to-spawn) **and lacks cycle-awareness.**

The fundamental fix unifies them: **(a)** anchoring computes the producer's scaled
clock **only to the target's spawn** (minimal, correct — Approach 2's core), and
**(b)** the anchor path shares the same `inFlight`/visiting cycle-break as the
wait-join path, so *any* genuine dependency cycle (real unfaithful emit) is broken
once, loudly, by the recorded back-edge — from whichever path reaches it. That is
one anchoring rule (counterfactual prefix-to-spawn) + one cycle mechanism, instead
of today's over-reaching anchor with a half-applied break.

### Another alternative worth weighing: ordered (topological) replay

Compute a dependency order (spawn-edges + wait-edges) **once** (it is
factor-independent, so it amortizes across all hundreds of simulations) and replay
ops so each is reached only after its producer's relevant prefix and its wait
targets — eliminating *out-of-order* anchoring entirely; a genuine cycle then shows
up as a topological-sort failure (= unfaithful data, flag it). This is the
conceptually cleanest "no anchor path at all" fix, but it is a larger rewrite of the
lazy `finish()` core, and it still needs "producer's clock *at* the spawn" (not just
the producer's finish), so it does not escape the prefix-to-spawn requirement — it
just reorders when it is computed. I'd treat it as the more-invasive option to
consider only if the resumable-replay state proves messy.

## 4. What the owner must decide

1. **Accept the design correction:** counterfactual-frame anchoring is the model
   (in-order *and* cross-tree); original-frame is a flagged last-resort only. (I
   believe this is forced by Test A + Test B — not really optional.)
2. **Choose the fix shape:** Approach 2 (scaled prefix-to-spawn) as the surgical fix
   vs. the topological rewrite — accepting that *both* require "producer clock at
   spawn" and that Approach 2 needs resumable-replay or a parallel `clockAtSpawn`.
   My lean: Approach 2 + unified cycle-awareness (smaller, sufficient, accurate).
3. **Confirm the shared-replay landing:** the fix changes native too (it shares the
   bug); accept the premise amendment + the §6.1/§6.3/§3.0.1 reconciles.
4. **Require regression coverage before landing:** the config-parse what-if as a
   *correctness* test (asserts the cross-tree saving is credited — the exact thing
   Approach 1 fails), **plus** a synthetic concurrent-cross-subtree-singleflight
   structure asserting `CycleWarnings == 0` after the fix and `> 0` before. Both are
   buildable without the 9.3 MB trace.
5. **Evidence I'd still want** (cheap, for the record): the `no-anchor SCC = 0` and
   native-5-cycles outputs from the implementer (the two load-bearing empirical
   claims); not needed to accept this analysis, which is settled from the code.

---

## Summary

- **Original-frame anchoring: a bug, not a principle.** The replay already anchors
  in-order children in the counterfactual frame (`replay.go:375-378`); pinning only
  cross-tree targets is an inconsistent fallback approximation, and the config-parse
  what-if proves it returns the wrong answer. I **retract** my round-2 endorsement
  of Approach 1.
- **What the design must say:** an op's simulated start is its producer's
  *counterfactual* clock at spawn — uniformly for in-order and cross-tree targets;
  original-frame is a flagged last resort. Amend the "replay unchanged" premise; fix
  the "cycle ⇒ unfaithful emit" taxonomy to the two-way (emit vs replay) form with
  the native-cycle diagnostic; the §6.1 cycle invariant becomes *correct* (no seam).
- **Approach 2** (scaled prefix-to-spawn) is the right direction: correct on the
  example, removes the over-reach cycle, leaves only genuine cycles (caught by
  cycle-awareness) — at the cost of resumable-replay or a parallel `clockAtSpawn`.
  The **fundamental fix** unifies the three `finish()` entry paths: minimal scaled
  prefix-to-spawn anchoring + one shared `inFlight` back-edge for any genuine cycle.
  Topological-order replay is a cleaner-but-bigger alternative that still needs
  "clock at spawn."
- **Owner decides:** accept the corrected principle; pick Approach 2 (my lean) vs
  the topological rewrite; confirm the shared-replay landing + reconciles; require
  the config-parse correctness test + the synthetic cycle test before landing.
```
