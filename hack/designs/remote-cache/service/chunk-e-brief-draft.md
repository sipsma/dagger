# Chunk E implementer brief — DRAFT (spawn after chunk C lands + conformance package consumable)

**Base**: fork from chunk D implementer's worktree (or its successor tip if chunk F merges
first — coordinate; dagger.io repo). Boilerplate from prior briefs verbatim.

**Scope (design §10.5, §12 rung 4, §13 T-S9 rung 4 + T-S10 + e2e warm proof + growth
gate):**
1. **Keeper export lane**: engine_cache_commands schema re-scoped to kind=export only
   (port 00097 pattern; import is config-driven and command-free by design — §10.5);
   keeper claim loop → engine admin API POST /v1/cache/export → record outcome;
   export-then-teardown sequencing in the keeper idle path; command failures mark
   failed + event-logged, never fail anything else (S4).
2. **Checks integration**: enqueue export-after-green in api/checks (salvage run.go
   integration + API_CHECK_NAME_ALLOWLIST knob retained for affordable live loops);
   export never gates check success (S4).
3. **Provisioning config injection**: cache-service env (URL, org token, scope, budgets)
   into engine provisioning for cloud-managed engines; scope convention = repo identity.
4. **Subscription-gating decision consumption**: the escalated product question
   (EnsureActiveSubscription on cache routes) — implement whatever ruling has landed by
   spawn time; if none, STOP-AND-DISCUSS at surface-map time.
5. **The e2e full-stack suite** (rung 4): API server (cache service enabled) + Postgres +
   MinIO + keeper + ≥2 dev engines, all inside Dagger per the dagger-repo integration
   style. Runs: the conformance suite (T-S9 rung 4) against the mounted service; T-S10
   (export interruption mid-blob-upload ⇒ bundle valid + selectable, warm run green w/
   fall-throughs, next export completes the gap; GC-safety placeholder until GC slice);
   the cross-engine warm proof through the REAL service (chunk C's integration shapes,
   re-based on the service instead of the test service); the ≥3-cycle growth gate.
6. **Migration renumbering** happens at THIS chunk's landing (00098/00099/(00100 facts?)
   against live main).

**Gate**: e2e suite green + conformance rung 4 + T-S10; chunk D suites stay green.

**Known facts**: engine images for e2e come from the dagger repo's engine-dev toolchain —
coordinate with me for the cross-repo image handoff (the e2e needs an engine build
containing chunks A+B+C; likely via a local image export the dagger.io suite consumes —
this is the first genuinely cross-repo test wiring, expect surface-map questions).
Chunk C's conformance package export shape: RunConformance(t, baseURL, token)-style,
dependency-light (verify as-built before writing the e2e harness).
