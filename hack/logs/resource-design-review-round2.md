# Resource-aware simulation design — adversarial review round 2 (triage record)

Reviewer: same fresh Codex XHIGH (tc-b954a3db0931e8534f2c73d6356ecc05), turn
019f3946-9d5c-70d0-9b5a-2e0422f19930, verdict ROUND-NEEDED.
9 findings: 2 BLOCKER, 4 MAJOR, 3 MINOR. All ACCEPTED; F1's resolution
differs from the reviewer's first-listed option (declared no-suspension
instead of interval-cutting), with the doctrine argument recorded below.

| # | sev | verdict | resolution |
|---|-----|---------|------------|
| 1 | BLOCKER | ACCEPT (resolution: no-suspension, declared) | Confirmed: nested-client ops are siblings of the phase chain (graph.go:489-502) so processRun self segments do NOT subtract them — the round-1 text was internally inconsistent about this. Chosen semantics: demand CONTINUES through sibling nested overlap, because the recorded data carries no container-side blocking edge (async nested calls are common) — cutting the intervals would invent a blocking fact, the exact compensation doctrine forbids. "Demand inactive while blocked" narrowed to recorded waits/children OF dilation-set ops (what SelfSegments subtracts). §3.3 + §3.5 rewritten; V-R19 narrowed; V-R21 added with the real sibling topology. |
| 2 | BLOCKER | ACCEPT | The "spawn anchor dilates" claim was not implementable over unchanged programs (spawn lives on exec.run whose own self time is ~empty). Replaced with an explicit per-exec window transform M_e: monotone map from recorded window-work position to sim time, built by the loop; non-exec_phase children of dilation-set ops spawning inside the window anchor at M_e(t) via deferred spawns; truncated-window deferred spawns fire at the window end image + defensive counter (G6 family). §3.5 + Figure 3 caption + V-R12 rewritten. |
| 3 | MAJOR | ACCEPT | W′ = f·W introduced as the simulated conservation target; G7 checks against W′; raw W = provenance only. §4.2 + G7 + V-R16. |
| 4 | MAJOR | ACCEPT | Fragment rounding rule: fragment lengths = differences of cumulative rounded positions, telescoping to the once-rounded int64(float64(dur)×f) — nanosecond-exact C′=∞ equivalence for any fragmentation. §4.2 + V-R15 (incl. the 3ns/f=0.9 case). |
| 5 | MAJOR | ACCEPT | G5's d_i limb gets the same measured-floor tolerance discipline: refuse only above C_rec·(1+τ_d), residual below; τ_d set by CAL-1. §10. |
| 6 | MAJOR | ACCEPT | Coverage context split into two defined numbers: wall-clock coverage = |union of modeled demand intervals|/makespan (≤1, cannot be masked by parallelism) and work intensity = ΣW/(C_rec·makespan). §8.3 + Figure 7 caption. |
| 7 | MINOR | ACCEPT | W1 figure producers relabeled d≈0 (token height), note fixed to "never exceeds 3 (producers d=0)"; catalog scenario states producers d=0 exactly. |
| 8 | MINOR | ACCEPT | §7: all observed constraint components are loaded/emitted with presence flags; the provenance enum explicitly means only the BINDING constraint. |
| 9 | MINOR | ACCEPT | Appendix A containment demoted to a verify-at-implementation precondition with an emit-time gate; citations no longer imply code-proof. |
