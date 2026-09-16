# Batch 4 implementation report

**Status: blocked; the five-commit acquisition delivery is incomplete.** The branch contains implementation commit `e307838a698b7845e0bf743225281d90b01b01e1` (`dagql: add per-part writer gates and supplied lazy tasks`) and this separate evidence commit. Steps 2–5 are not delivered and this branch must not be treated as an operational per-part acquisition implementation.

The blocker is inline snapshot ownership across publication, encoding and decode. A parent-only probe shows two completed inline Directory outputs losing both owner links during list encoding, followed by decode failing with `zero result ID`. The converged design requires those outputs to keep their enclosing row, while preserving item-role scoping. The parent has no scoped role carrier or durable path mapping for that case. [BLOCKER.md](BLOCKER.md) records source references, the reproduction and two decision options. Option A preserves inline ownership by specifying and implementing scoped snapshot links; option B changes inline outputs into separately owned rows and requires changing the design.

## Inputs and parent

- Parent: `77f6279559061fd1bb6b3b18e6b08582c7b013a3`. The requested hard reset was performed before work. Parent commits were not changed.
- Commission: `10b43c933bce4afe4ee273332c79e15bcbd979c4:continuation-evidence/implementation-commissions/BATCH4-ACQUISITION-IMPL.md`.
- Acquisition design: `2ad7bff7a4f61e39361fd222230a21bf4ffb3569`, blob `b463258caa499307b5a44032664bae37aba11623`.
- Read the full engine-debugging skill and the specified producer, transfer, explanation, shared-resolution and integration-decision inputs.

## Implemented groundwork

- Row-owned `PartGateCell`, lazy gate allocation, full-address permits, finite writer drains, decision permits and task generation checks. `InstalledOutputs` is declared. No gate state is serialized.
- `RunLazyTask` uses the existing attempt kernel with an explicit supplied body, nonzero token, atomic installation record, `NoJoin`, owner-sync barrier and resumable cleanup/settlement callbacks. Synthetic completion does not set the native whole-result latch. Missing synthetic bodies fail unless resuming a retained continuation. Synthetic attempts hold their row through release and operation admission.
- Attached File/Directory and Container callbacks enter a stable host before the native body latch. Detached values keep their direct path. Native finality advances through installed state to complete after bookkeeping; native sync retries retain the original installation token.
- Data-only File/Directory kind routing and Container routing based on recorded field, metadata and targets. The builtin and import recipes route to a whole-result group. Volatile exec-hit routing retains its metadata/delegation shape. Raw routing does not resolve producer inputs.

`SourceCheck` currently has only gate-side groundwork; source-membership/revision validation belongs to the unfinished second step. The gate and host are not yet connected to acquisition activation or imported decode. No Prepare/Commit/Finish API, source selection, chain provider, private producer invocation, mixed raw Container view or acquisition publication writer is included.

## Changes to ordinary local execution

These are the ordinary execution paths touched by the groundwork:

1. `dagql/cache.go`, `evaluateGroup`/`runLazyTask`: native execution continues to derive the same callbacks and use the original attempt, waiter, cancellation, telemetry and retirement machinery. Attempts additionally carry a generation token and context value. Native installed-state bookkeeping retains its original token across sync-only retries. The ordinary lookup and e-graph selection implementations are unchanged.
2. `core/file.go` and `core/directory.go`, `LazyEvalFunc`: an attached callback now enters its row host before calling the captured native callback. A direct attached call goes through cache evaluation; detached callbacks run directly. Existing completion writers remain in place.
3. `core/container.go`, `LazyEvalFunc`, and `core/container_parts.go`, `runLazyGroup`/`evaluatePartsDirect`: attached execution enters the same row host; refined direct evaluation routes through cache part resolution. The admitted callback still owns the native latches and final-delegation sweep. Detached paths retain native execution.
4. `dagql/cache_part_host.go`: native body exit records installed output identities after core latches are released; only successful owner-sync/operation-lease completion marks them complete. These facts are groundwork for later source probing, not a currently enabled alternate acquisition route.
5. `dagql/cache_part_routes.go` and `core/part_routes.go`: new pure routing descriptions are available but do not alter ordinary graph lookup or independently start work.

These paths introduce host/gate and generation overhead. The commissioned allocation measurements have not been performed; no cost-bound claim is made.

## Verification

Final sequential commands are in [COMMANDS.md](COMMANDS.md).

- **PASS**, focused DagQL race selection: supplied-body isolation, missing-body refusal, continuation-only retry, barrier/NoJoin behavior, finite drain, plus unchanged native retirement, caller cancellation, bookkeeping retry and sibling concurrency regressions.
- **PASS**, focused core race selection: pure-vs-native routing cases, direct refined evaluation, concurrent completion clearing and refined/unrefined routing during lazy clearing.
- Parent-only inline-role probe: standalone encoding control passes; the two required inline assertions fail. The probe is a text artifact and is not installed as a failing test in this branch.
- `git diff --check` passes.

The full design §10 matrix, real snapshot-store/import fault controls, privileged mount-namespace runs, restart/acquisition checks, native allocation measurement and the cold two-engine module proof remain **unrun**. No chain, receipt or cold-module success is claimed.

## Delivery boundary

Only the first planned implementation commit is present. The final evidence commit contains this report, the blocker, exact focused commands, final test outputs and the reproducible parent probe. No pushes, pull requests, tags or contacts with authors/reviewers were made. No foundation contract was silently widened and inline acquisition was not silently excluded.
