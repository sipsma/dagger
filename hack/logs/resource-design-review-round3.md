# Resource-aware simulation design — adversarial review round 3 (triage record)

Reviewer: same Codex XHIGH (tc-b954a3db0931e8534f2c73d6356ecc05), turn
019f3954-e0d9-7643-8f5d-ba19bebc0e4d, verdict ROUND-NEEDED.
7 findings: 2 BLOCKER, 5 MAJOR. All ACCEPTED.

| # | sev | verdict | resolution |
|---|-----|---------|------------|
| 1 | BLOCKER | ACCEPT | M_e vs G8 contradiction resolved by scoping G8 honestly (§3.5): (1) graphs without capacity data (= all existing replay tests) bit-for-bit always; (2) demand-carrying baselines at C′=∞ bit-for-bit (M_e = identity); (3) under hypothesis factors at C′=∞, bit-for-bit EXCEPT M_e-remapped anchors + downstream, which assert hand-derived values (new V-R22 asserts the divergence itself). Root insight adopted into the doc: the infinite mode does not move nested spawns when a factor scales their host — an existing modeling gap this design exposed; M_e is a refinement at every C′; back-port named as a §14 seam, out of scope. |
| 2 | BLOCKER | ACCEPT | Deferred spawns are progress milestones = first-class projected events, (re)projected at every rate change with the same generation-counter invalidation as completions (§3.5 case 1). |
| 3 | MAJOR | ACCEPT | M_e defined over recorded instants, case by case: fragments (milestone projection) vs gaps (linear from the gap's sim start image, clamped to its sim end image; clamps counted, G6 family). Gap definition corrected: window minus D_e; exec_phase child intervals are NOT gaps (traversal descends); sibling nested overlap is NOT a gap. |
| 4 | MAJOR | ACCEPT | No-suspension propagated everywhere: §3.3 honesty box (uniform placement incl. sibling overlap), §3.5 blocked-demand bullet (gaps in D_e), §8.1 parenthetical, §11.4 note. The wrong "any child interval of a dilation-set op is a gap" sentence removed. |
| 5 | MAJOR | ACCEPT | pendingAnchor state specified: global state owned by the exec's window progress; registering parent moves on; implicit joins treat pendingAnchor children as unfinished (block until fired+finished); no-deadlock argument (window progress carried by phase coroutines, never waiting on the exec op's later actions; unfaithful cycles → G6). |
| 6 | MAJOR | ACCEPT | Conservation unified: W′ = d·T′ where T′ = Σ once-rounded factor-scaled dilated fragment lengths — derived from the SAME rounding the time arithmetic uses, so G7 is exact by construction and cannot conflict with G8 rounding. §4.4 stale "measured W" wording fixed; ≈f·W relation stated as up-to-rounding. |
| 7 | MAJOR | ACCEPT | HTML re-synced: Figure 7 caption now carries the union wall-clock coverage + work intensity metrics; HTML §11.4 gained the placement + teardown-tail notes; all round-3 semantics mirrored (M_e definition block, G8 scoping block, W′ rows, V-R22, §14 seam). |
