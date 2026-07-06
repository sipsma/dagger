# Resource-aware simulation design — adversarial review round 1 (triage record)

Reviewer: fresh Codex XHIGH (tc-b954a3db0931e8534f2c73d6356ecc05), turn
019f3931-eed8-7610-9ec3-44c43b0459e5, verdict ROUND-NEEDED.
17 findings: 3 BLOCKER, 8 MAJOR, 4 MINOR, 1 NIT (numbering below is the
reviewer's). Triage: per-finding; all 17 ACCEPTED (F5 accepted as a declared
simplification with rationale rather than the reviewer's gating framing).
Fixes applied to BOTH hack/designs/resource-aware-simulation-design.md and
the HTML page before round 2.

| # | sev | verdict | resolution |
|---|-----|---------|------------|
| 1 | BLOCKER | ACCEPT | §3.1: C_rec = min over every present constraint (quota, cpuset, NumCPU); provenance lists each component and which bound; V-R20 added |
| 2 | BLOCKER | ACCEPT | C_rec vs C′ terminology introduced (§3.1); G5 checks against C_rec only; downward-grid d>C′ is valid stretch input; V-R18 added |
| 3 | BLOCKER | ACCEPT (verified in code first) | exec.containerStart/exec.processRun are grandchildren via the runContainer phase ctx (executor_spec.go:1429-1434). Dilation set + SW = exec op + ALL exec_phase descendants; no-double-count argument stated; Figure 3 redrawn with the grandchild row; V-R17 added |
| 4 | MAJOR | ACCEPT | §8.1: S(t) over in-window self segments (identical intervals to the sim); V-R19 added |
| 5 | MAJOR | ACCEPT as declared simplification | aggregate-W placement cannot be derived; every denominator assumes a placement; declared in §3.3 + §11.4 with B2 as the evidence-triggered remedy (gating on "unsupported shape" is undetectable from an aggregate counter) |
| 6 | MAJOR | ACCEPT | exact factor rule defined (§4.2): f scales dilated time AND W share, d invariant; G7 target scales; V-R16 rewritten with the exact math |
| 7 | MAJOR | ACCEPT | WindowEndNS = process-exit boundary (callWithIO return, executor_spec.go:1411-1424 — the processRun end instant); file reads stay in cleanup; G4 cross-check emitted window == recorded processRun interval |
| 8 | MAJOR | ACCEPT | verdict layer 2 scoped to the measured exec population + always-printed coverage context + refusal of the global phrasing when coverage is small (§8.3, Figure 7) |
| 9 | MAJOR | ACCEPT | relation gate corrected to per-schedule makespan_cap(h) ≥ makespan_inf(h); savings-difference inequality explicitly disclaimed as false in general (§13, V-R10) |
| 10 | MAJOR | ACCEPT | G5 now refuses above the declared tolerance (below = printed residual); doctrine restored |
| 11 | MAJOR | ACCEPT | CAL-2 reverse restated: stretch-only predicts ~no relief; the comparison quantifies the under-prediction gap; PSI-vs-realized-speedup added as characterization only |
| 12 | MINOR | ACCEPT | boundary splits live in a capacity-mode-only precompute overlay; shared compiled program untouched (§4.2) |
| 13 | MINOR | ACCEPT | V-R17..V-R20 added |
| 14 | MINOR | ACCEPT | markdown corrected to two re-projections (matches figure) |
| 15 | MINOR | ACCEPT | citations fixed: io.pressure const :17 + read/parse :122-138; PSI full gauge :129 parse :159-161 |
| 16 | MINOR | ACCEPT | E-R1 path resolution = OCI spec path joined under /sys/fs/cgroup exactly as resources.NewSampler (sampler.go:14,40) |
| 17 | NIT | ACCEPT | open op + hit-short defined in §2 |
