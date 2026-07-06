# Chunk 4 cycle fix — round 3 review (design author)

Review of `hack/designs/wcprof-otel-chunk4-cycle-FUNDAMENTAL-implementer.md` (the
empirical prefix-spawn fix + the residual skip-predicate), against my worktree's
`engine/wcprof/wcanalyze/replay.go` and the design. Erik has settled the decision
(adopt prefix-to-spawn in shared `wcanalyze`); my charge is to stress the
*remaining* concerns — chiefly the emit-vs-replay question, which is about my own
§3.1 rule. Analysis only; I do not edit the design doc.

The empirical work is solid and the headline conclusions match my round-2
analysis reached independently: prefix-to-spawn is correct on config-parse (100 ms,
where `startOf` regresses to 0) and removes the over-reach cycle; native cycles
identically. I focus on whether the **residual + its skip-predicate** are sound and
correctly layered.

## 1. EMIT vs REPLAY (my lead) — §3.1's attribution IS faithful; fix belongs in the replay

**The residual, restated precisely.** op#250 is a `call_exec`. Its resolver did
*concurrent* work: one branch made a telemetry-**suppressed** sub-call that joined
`D`, while another branch spawned op#251 (at 190 ms, *before* the D-join's wait
ended at 205 ms). Per §3.1's suppressed-caller rule, the suppressed sub-call's wait
has no span of its own, so it lands on the current ancestor span — op#250's
`call_exec`. Naive prefix-spawn, replaying op#250 up to op#251's spawn, sees that
D-wait's *start* precede the spawn and serializes it → reaches D → D waits A →
cycle.

**Is homing that wait on op#250's `call_exec` faithful? Yes.** op#250's resolver
genuinely blocked on D (via the suppressed concurrent branch), and op#250's
`call_exec` does not finish until its resolver — *including* that branch —
completes. So "op#250 waited on D" is a true statement *about op#250's finish*:
op#250 finishes after D. Native agrees — it just records the wait on a separate
per-caller `call` op (a child of op#250) instead of on op#250's `call_exec`
directly, and op#250 still finishes after D via the implicit join over that child.
The two structures encode the **same** finish-dependency. So §3.1 is not inventing
a dependency; op#250→D is real.

**Then why does only OTel surface the residual?** Pure *structure*, not
faithfulness. In native the D-wait lives *inside a child* of op#250, so op#250's
prefix-to-op#251-spawn never processes it (the child ends at 205 > op#251's spawn at
190, so op#250's `joinUpTo` — which only joins children ending ≤ the spawn time —
doesn't reach it; `replay.go:353-369`). In OTel the same wait is op#250's **own**
action (ancestor-homing), so the prefix walk encounters it directly. The
skip-predicate (`endNS[target] ≤ startNS[spawn]`) makes the OTel direct-wait behave
exactly like native's child-wait: a wait that didn't finish before the spawn didn't
gate it, so it isn't serialized into the spawn. **This is the replay aligning two
faithful-but-different encodings under one correct rule — not masking an emit bug.**

**Should it be fixed in emit instead? No — and not because it's convenient.**
- The wait *must* be emitted (op#250→D is a real finish-dependency; dropping it
  re-opens Break #1). So emit cannot omit it.
- Homing it on the ancestor is the *deliberate* §3.1/§4.1 volume tradeoff (no
  per-caller span for suppressed callers). Matching native's per-caller-op structure
  in OTel — the only emit change that would dodge the residual — is exactly the
  per-caller-span approach §4.1 rejected for volume. So "fix in emit" means
  reverting a settled, sound design decision.
- The skip-predicate is **correct for native too** (a concurrent wait never gates a
  spawn, regardless of source); native simply didn't trigger it on this trace. So
  it's a general replay-correctness rule, rightly landed in shared `wcanalyze`.

**Verdict:** §3.1's ancestor-homing is faithful (it correctly gates op#250's
*finish*); the residual is a general prefix-spawn gating-semantics question; **fix
in the replay, not emit; it is not a forbidden seam.** The one honest caveat — to
record in the design, not to fix — is that ancestor-homing *conflates* "a concurrent
branch of the ancestor blocked" with "the ancestor blocked," so the wait gates the
ancestor's finish but must **not** gate the ancestor's concurrent spawns; the
replay's gating predicate is what enforces that distinction. (Native has the
analogous imprecision via the child op, so the two sources stay consistent.)

## 2. Skip-predicate soundness — correct, with one exactness nit

`endNS[target] ≤ startNS[spawn]` (process the pre-spawn wait only if it finished by
the spawn) is the right gating test:
- **It cannot skip a genuine gating wait.** A wait that gated the spawn means the
  parent unblocked *then* spawned, so the spawn's recorded start ≥ the wait's
  recorded end ⇒ `endNS ≤ startNS` ⇒ processed. A wait still in flight at the spawn
  (concurrent, didn't gate) has `endNS > startNS` ⇒ skipped. Exactly right.
- **Sound under counterfactuals.** It keys on *recorded* times, which is precisely
  how the rest of the replay treats dependency *structure* (the implicit join keys
  on recorded `endNS`, `replay.go:356`; `actWaitNoop` on recorded waitEnd vs
  targetEnd). The replay's standing assumption is "recorded dependency structure is
  invariant under the hypothesis; only self-times scale." The predicate is in that
  family — gating is a structural fact taken from the recording. So it's as sound as
  the implicit join itself.
- **It's correctly scoped.** The skip lives only in `spawnTo` (the prefix anchor),
  not in the full `finish` (`replay.go:379-382` keeps processing all waits). So
  op#250's *finish* still waits on D (correct); only op#251's *spawn* is freed from
  the concurrent wait (correct). Precise.
- **Exactness nit (worth fixing for validated native code):** the predicate uses
  `endNS[a.ref]` (the wait *target*'s recorded end) as a proxy for the *wait*'s
  recorded end, because the compiled `action` stores only the wait's `at` (start),
  not its end (`action{at,dur,ref,kind}`, `replay.go:55-60`). For a JOIN, waitEnd ≈
  targetEnd within `joinEpsilonNS` (1 ms), so the proxy is exact-enough and
  unambiguous on the real residual (205 vs 190). But landing an inexact temporal
  predicate in PR #13393's validated replay invites a 1 ms-boundary edge case;
  **thread the actual recorded waitEnd through the compiled action and compare that**
  — cheap, and removes the only fuzz.

## 3. Residual taxonomy — two tiers are correct AND complete

- **Tier A — genuine dependency cycle → break + report.** A real cycle in the
  {spawn-gating + wait} DAG (a deadlock that "shouldn't exist" = unfaithful
  emit). Caught by the `inFlight` break (`replay.go:341-345`); flagged by §6.1.
- **Tier B — concurrent non-gating pre-spawn wait → skip.** A wait that didn't
  finish before the spawn; not a dependency of the spawn; skipped in the prefix.

**Complete, and Tier B cannot mask Tier A.** The skip only suppresses a concurrent
wait *during prefix-spawn anchoring*; the **full** `finish(par)` still processes
that same wait with no skip-predicate, so if its target is in a genuine cycle, the
`inFlight` break still fires there. I verified `spawnTo` itself sets
`inFlight[par]` during its prefix walk (proposed code) — so a genuine cycle reached
*through the prefix* is also caught, not silently skipped. So: artifact cycles
(over-reach) eliminated by prefix-to-spawn; concurrent non-gating waits handled by
Tier B; genuine cycles still surfaced by Tier A from both the prefix and the full
finish. The cycle signal becomes *clean* (fires only on real unfaithful data) —
which is the right way to satisfy "no seams": the §6.1 invariant is made correct,
not relaxed.

One subtlety the landed code must preserve (and test): `spawnTo` walks the parent's
prefix with the skip-predicate, and `finish(par)` later re-walks it *without* — so
the target's *start* uses the skipped clock while the parent's *finish* uses the
full clock. That dual-semantics double-walk is the crux of the correctness;
`setStart` idempotency keeps the target on the (correct) prefix-start. A future
"optimization" that caches the prefix clock and reuses it for `finish(par)` would
silently break the scoping. **Add a test that asserts a concurrent pre-spawn wait
gates the parent's finish but NOT the target's spawn**, to lock this in.

## 4. Doc reconciles this fix forces (specify; do not edit yet)

- **"Reuse the validated native replay UNCHANGED" premise → dead.** Replace with:
  "reuse the native replay, fixing the genuine *shared* bugs the OTel work surfaces
  — Chunk 4: the anchor over-reach + the concurrent-wait spawn-gating — landed in
  shared `wcanalyze` (native + PR #13393)."
- **`replay.go:31-34` original-frame comment → rewrite.** It currently rationalizes
  original-frame anchoring as intended; that's the bug (round-2 analysis). New text:
  out-of-order anchoring replays the producer **with the factor up to the target's
  spawn** (counterfactual-frame, consistent with in-order `actSpawn`); a pre-spawn
  wait gates the spawn only if it recorded-finished before it; recorded-offset
  survives **only** as the rare in-flight-parent fallback.
- **§3.1 → add the replay-interaction note.** A suppressed caller's wait homed on
  the ancestor's `call_exec` (the §3.1/§4.1 volume choice) may be *concurrent* with
  the ancestor's other work: it gates the ancestor's **finish**, not the ancestor's
  concurrent **spawns**; the replay's prefix-to-spawn gating predicate enforces
  this, keeping OTel's ancestor-homed structure equivalent to native's per-caller
  structure. So §3.1 stays as designed; the design just records the interaction.
- **§1.1/§2.5/§6.3 cycle taxonomy → the two-tier + diagnostic** (carried from round
  2): a cycle is *either* unfaithful emit *or* a replay anchor/gating artifact; the
  "does native cycle on the same data?" diagnostic discriminates; with the fix,
  `CycleWarnings` is a clean unfaithful-emit signal.
- **§6.1 → `FallbackAnchors`.** The fix drives it to 0 (prefix-spawn always
  anchors). Either retire the signal or have `spawnTo` increment a refined counter
  *only* on the in-flight/self-ref recorded-offset fallback (the one place the
  approximation survives) so it stays visible. Update `gate.go` +
  `TestGateFallbackAnchorsReportOnlyAndThreshold`.

## 5. Remaining blockers / design-consistency

- **[BLOCKER] Full native test suite, not just chunk fixtures.** This lands in
  validated PR #13393 code. The writeup reports chunk2/3/4 oracle/ranking +
  `wcanalyze`'s counterfactual fixtures pass and one expected gate-test failure —
  but does not state that the full native `wcanalyze` (`replay_test.go`,
  `report.go` paths) and `engine/wcprof` suites pass. Run them and report
  per-test; that is exactly Erik's "stress-test before it lands in validated native."
- **[BLOCKER] Land the regression tests with the fix:** (a) config-parse what-if as
  a *correctness* test (asserts 100 ms — the exact thing `startOf` failed); (b) the
  9-op `buildMinCycleGraph` asserting CycleWarnings `0` after / `>0` before; (c) the
  dual-semantics test from §3 (concurrent pre-spawn wait gates finish, not spawn).
  These currently live in an env-gated throwaway and were reverted — they must ship.
- **Exactness:** thread the real waitEnd into the action and compare it (rather than
  the `endNS[target]` proxy) before this enters validated native code (§2).
- **The surviving recorded-offset fallback** (in-flight parent, `spawnTo` proposed
  lines 186-188) is the *one* place the wrong-counterfactual approximation remains.
  Confirm it is genuinely rare (a real re-entrancy) and route it through the refined
  counter so it's observable if it grows.
- **Re-baseline the §6.4 standing gate.** The fix *changes the answer toward
  correctness* (`:uploading…` surfaces at 688 ms; `ModuleSource.asModule` 209→376
  ms) — good, but any pinned oracle/ranking baselines from before the fix are now
  stale and must be recaptured post-fix.
- **Perf at scale.** `spawnTo` re-walks a parent's prefix per out-of-order
  reference, and the parent prefix is walked again at full finish. Negligible at 11k
  ops (0.18 s); flag for multi-million-op traces (the replay's stated target) — a
  memo on the prefix clock keyed by (op,factor) could bound it *if* it ever
  regresses, but only if it preserves the dual-semantics (§3).
- **Services / §3.4 unaffected.** The service.start erasure (round 2) is a separate,
  still-open emit fix; this replay change doesn't touch it, and the service fixtures
  pass. No new disturbance.

## Summary

- **Emit-vs-replay verdict:** §3.1's suppressed-caller-wait-on-ancestor attribution
  **is faithful** — op#250's `call_exec` genuinely waits on D for its *finish*
  (native encodes the same dependency via a child op). The residual is a general
  prefix-spawn *gating* question, not an emit mis-homing; **fix in the shared replay,
  not emit**, and it is **not** a forbidden seam (the predicate is correct for native
  too — native just didn't trigger it). Add a design note recording the §3.1↔replay
  interaction (ancestor-homed concurrent waits gate finish, not concurrent spawns).
- **Skip-predicate:** `endNS[target] ≤ startNS[spawn]` is the correct gating test —
  never skips a genuine gating wait, sound under counterfactuals (recorded-structure
  family), correctly scoped to anchoring (not full finish). One nit: thread the real
  waitEnd instead of the target-end proxy for exactness in validated code.
- **Taxonomy:** two tiers (genuine cycle → break/report; concurrent non-gating →
  skip) are correct and complete; Tier B can't mask Tier A (full finish + `spawnTo`
  inFlight both still catch genuine cycles).
- **Doc reconciles:** premise dead; rewrite `replay.go:31-34`; add §3.1 interaction
  note; two-tier cycle taxonomy + diagnostic; `FallbackAnchors` retire/refine.
- **Remaining blockers:** run the *full* native suites and report per-test; land the
  three regression tests (config-parse correctness, min-cycle, dual-semantics);
  thread the exact waitEnd; observe the surviving recorded-offset fallback;
  re-baseline §6.4; watch perf at multi-million-op scale. With those, this is the
  correct fundamental fix — accuracy-preserving, no seam, cycle signal made clean.
```
