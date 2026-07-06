# wcprof x OTel Chunk 4 first-principles review

Reviewer: Codex  
Scope: review only. I read `wcprof-otel-chunk4-firstprinciples-handoff.md` and `wcprof-otel-chunk4-items12-e8c0dfe498.patch` first, then checked `git show e8c0dfe498`.

## Alignment

I agree with the governing principle. The analyzer should be a rational function of the recorded graph. It should not infer missing causality from timestamps, and it should not compensate for missing structure with recorded-offset approximations. If the rational model gives an odd answer, that is either the correct answer for the data or evidence that emit/data is incomplete.

This changes how I would frame my prior `FallbackAnchors` recommendation. Hard-failing fallback anchors is a valid interim safety gate while the fallback exists, but it is not the principled final model. The principled fix is to remove the fallback and make root/on-demand anchoring exact from recorded data.

## Items 1 & 2

### Item 1: zero-duration wait-target fix

Verdict: correct and scoped.

The bug is real: `joinUpTo` runs before same-time actions, so a zero-duration child with `StartNS == EndNS == t` could be implicitly anchored before the gate at `t`, then later re-anchored by its spawn. When that zero-duration child is itself a wait target, the wrong early finish propagates downstream.

The fix is rational:

- `joinUpTo` now detects an unstarted child whose spawn is pending at the same instant and returns without advancing `pendCur` (`engine/wcprof/wcanalyze/replay.go:462-487` at `e8c0dfe498`). That lets the same-time max gate run first.
- `actionRank` now orders max-gates, then spawn, then self (`engine/wcprof/wcanalyze/replay.go:175-198`). That anchors a zero-duration child at the same timestamp as self, instead of after self.

The scope argument holds. A normal nonzero child interval removes that instant from the parent's self segments, so spawn-before-self is observable only for zero-duration children. The tests cover both faces: zero-duration child at a join wait end, wait-target propagation to makespan `300ms`, and zero-duration child at self start (`engine/wcprof/wcanalyze/replay_cycle_test.go:283-403`).

Low cleanup: the `SimStartConflicts` comment in `replay.go` still describes the old benign zero-duration conflict as a live source (`engine/wcprof/wcanalyze/replay.go:307-317`). After this fix, that should be updated; the commit message and tests say the signal is now clean.

### Item 2: fallback-anchor hard fail

Verdict: correct as an interim gate, but a band-aid under the governing principle.

The code does what it claims. `CheckStructural` fails when `FallbackAnchors > MaxFallbackAnchors`; with default `MaxFallbackAnchors == 0`, any fallback anchor fails (`engine/wcprof/wcotel/gate.go:21-33`, `engine/wcprof/wcotel/gate.go:143-145`). The test verifies default hard-fail and explicit opt-out (`engine/wcprof/wcotel/gate_test.go:227-258`).

As a gate around the current model, this is legitimate: `spawnTo` still falls back to recorded offsets for `par < 0`, in-flight ancestors, or missing parent/child spawn (`engine/wcprof/wcanalyze/replay.go:522-584`). Since a finite what-if sweep cannot prove those approximations harmless, enforcing on the precondition is better than accepting silent wrong rankings.

But first principles say the fallback itself should not exist. A root's recorded start is not a fallback; it is the data. A non-root whose parent prefix cannot reach its spawn is either a real recorded cycle/inversion or insufficient/invalid data. The analyzer should report that, not approximate an answer.

So item 2 should be treated as a temporary guard until item 3 removes the compensating paths. After the rational model lands, `FallbackAnchors` should be zero by construction or deleted. If any residual "in-flight ancestor inversion" remains, it should be an explicit replay/data error, not a fallback counter with an opt-out.

## Item 3: Rational Root Model

The rational model is:

- A root has no incoming causal edge. Its start is an exogenous recorded fact. Anchor it at its recorded start.
- A recorded parent/child edge or wait edge is causal data. Honor it.
- Do not chain roots by temporal order.
- Do not preserve idle gaps as inferred dependencies.
- Do not use recorded-offset fallback for a non-root. If the parent prefix cannot reach the child spawn, report a graph/model error or a real cycle.

In current code, `Run` still performs root chaining from temporal order (`engine/wcprof/wcanalyze/replay.go:360-391`), and `spawnTo(par < 0)` still records a fallback anchor (`engine/wcprof/wcanalyze/replay.go:527-538`). Both are the anti-pattern described in the handoff.

### Case A: concurrent cross-root dedup

Data:

- `R_A` and `R_B` are roots, both recorded with start `t=0`.
- `R_B` does setup, then spawns `T=load foo` at `t=100`.
- `R_A` has a recorded wait edge to `T`, unblocking at `t=300`.

Logical answer:

- Both roots keep start `0`.
- Scaling `R_B` setup from `100ms` to `0` moves `T`'s spawn from `100` to `0`.
- `T` finishes at `200`.
- `R_A`'s wait unblocks at `200`.
- Makespan goes from about `300` to about `200`.

The rational model gives this answer if roots are independently anchored and the cross-root wait is honored. The current fallback/chaining model obscures this by treating an unscheduled root as a fallback case.

Emit fix: none, if the data really includes both roots and the wait edge to `T`. This is exactly the data the model needs.

### Case B: sequential CLI roots

Data:

- Root `B` starts after root `A` ends.
- There is no recorded edge `A -> B`.

Logical answer for the data:

- `A` and `B` are independent exogenous roots.
- Scaling `A` does not shift `B`'s start.
- The total observed trace span still includes the recorded gap/sequence, but the counterfactual model must not infer that `B` causally depended on `A`.

The current chaining model is an inference: it guesses a causal dependency from temporal order. If the product wants "speeding up A would start B earlier" for a sequential CLI script, the data must record that. Otherwise the rational answer is that the engine trace does not know why B started later.

Emit/data fix:

- If the same Dagger client/session orchestrates both roots and the launch of `B` is causally caused by completion of `A`, emit an explicit sequencing/launch edge.
- If the serialization is outside the engine, for example a shell script running two separate commands, that dependency is outside the engine trace unless the CLI/Cloud model records it as a higher-level workflow edge.

### Case C: sub-session launched by a parent session

Data possibilities:

- If `R_A` launches `R_B` and the data records that launch, then `R_B` is not a pure independent root. It should be anchored through the recorded edge. The existing graph builder already has a form of this for nested clients: `nested_client` links reparent roots under the hosting exec (`engine/wcprof/wcanalyze/graph.go:270-293`).
- If the launch is not recorded, the data says `R_B` is independent. The analyzer should anchor it at its recorded start and not infer the launch dependency from timing.

Logical answer:

- With a recorded launch edge, scaling pre-launch work in `R_A` shifts `R_B`'s start through that edge.
- Without a recorded launch edge, scaling `R_A` does not shift `R_B`; if that looks wrong, the emit is missing the launch edge.

Emit/data fix:

- Record sub-session launch as a parent/child edge, `nested_client` link, or explicit wait/launch edge with enough timing to anchor `R_B` from `R_A`'s launch point.
- Do not add analysis fallback to guess this from overlap or timestamps.

## Model Code Changes Implied

1. Replace root chaining in `Run` with independent root starts at recorded `StartNS`.
2. Allow out-of-order references into an unscheduled root by anchoring that root at its recorded start as an exact root fact, not as a fallback.
3. Keep prefix replay for non-root children with recorded parents.
4. Remove recorded-offset fallback for non-roots. If `spawnTo(parent, child)` cannot reach the child's spawn, return a replay/data error or report a genuine cycle/inversion.
5. Reframe `FallbackAnchors`: before item 3, hard-fail as an interim guard; after item 3, it should be unnecessary. Any remaining occurrence is an internal bug or unsupported data shape, not a tolerated analysis mode.

## Final Answers

- **Items 1 & 2 verdict:** item 1 is correct; item 2 is a correct interim hard gate but not the final principled fix.
- **Gate role after rational model:** `FallbackAnchors` should be dissolved by the model change. If any fallback-like condition remains, it should be a hard replay/data error. The gate should not be the long-term mechanism that makes an approximation acceptable.
- **Rational model:** roots are independent recorded starts unless the data records an incoming causal edge; recorded edges are honored; no chaining; no fallbacks.
- **Three test cases:** concurrent dedup saves `100ms` across the root wait; sequential CLI roots do not shift without an explicit edge; sub-session shifts only if the launch edge is recorded.
- **Emit fixes:** record sequencing/launch edges for client/session/sub-session causality that should affect counterfactuals. If the dependency is outside the engine and unrecorded, the engine analyzer must not invent it.
- **Principle:** aligned. The one addition I would make is operational: the analyzer should distinguish "valid rational answer for this data" from "data insufficient for the user's broader question" in its diagnostics, but it should not compensate for the insufficiency.
