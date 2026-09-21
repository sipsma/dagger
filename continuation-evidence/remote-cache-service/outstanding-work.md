# Remote cache service: outstanding work after the demo

This document lists what is known to be unfinished, wrong, or unexplained after the 17 and 18 September demo work, and catalogues everything that exists on top of Erik's stacked pull requests with a suggested batching into further stacked pull requests. It is for Erik. Sources: the plan (`hack/designs/remote-cache/service-plan.md`, sections 14 and 19), the demo results (`demo-results.md`), the Namespace track's results (`namespace-demo-results.md` on the coordinator-namespace agent's branch), the rebase assessment (`continuation-evidence/remote-cache-service/REBASE-ASSESSMENT.md` on `rebase-engine-on-bf509625e7`), and the branches themselves as read on 18 September.

**Written against:** engine branch `engine-main` at `3587242fe7` (Dagger repo, on the predecessor's finished tip `bf509625e7`); service branch `service-seat-daggerio-8aeecb96` at `cf4266216` (dagger.io); Erik's stacked pull requests as listed by `gh pr list` on 18 September, top of stack `sipsma/remote-cache-deferred-filesystem-restoration` at `17f7dd89f4`. Nothing below is pushed except the pull requests.

Each item says who found it, what is known, and what is proposed. Items marked **Decision** need Erik. Items marked **Investigate** are not diagnosed.

## 1. Known issues

### 1.1 Engine

1. **Imported roots are the first victims of disk-pressure pruning.** Found in H3 (plan section 19, prune diagnosis at engine commit `ae53c6ac13`). A freshly imported root is a retained result with no session use and a last-use time equal to its import time, so under disk pressure the LRU prune evicts it before its first client. The harness pins never-reclaim GC bounds on nested engines, as the predecessor's own test does; production engines have default policies keyed on the root disk. Options: count an import as a use; exempt imported roots from the disk stage for a grace period. **Decision** on whether to change prune semantics; recommended for next week's production work.
2. **A clean-slate engine's own post-check export is cut off when its instance is destroyed** about 15 seconds after the check (Namespace runs 3 to 5). Harmless, since the service already holds A's results, but wasted upload work and a noisy `post bundle: context canceled` failure in every run. Options: the service skips export commands for engines whose only session was a hit-only rerun; or the API lets an exporting engine finish before destroy. **Decision**, lifecycle or policy.
3. **Builtin image blobs are a permanent disk floor.** E12 gives each builtin image's layer and config blobs one persistent lease, never released, so they accumulate across engine versions in one state directory. Prune's reclaimed-bytes estimate can count blob bytes that stay allocated. Acceptable for the demo, recorded in the E12 commit message; needs a release rule per engine version for production.
4. **A retained snapshot now also retains its compressed blob** while any owner holds it (E12), so imported layers occupy blob plus unpacked snapshot on disk. Trades disk for stable exports and no re-diffing. Should be measured on a real CI engine.
5. **Boot-time whole-cache wipe on a dangling persisted snapshot link** remains the predecessor's inherited policy (`dagql/cache_persistence_import.go`, their `boot-wipe/FINDING.md`). Their backing-snapshot fix removed one trigger. Not ours, not fixed; a production engine that hits it loses its whole local cache.
6. **`ErrPartNoProgress` is a new terminal demand outcome** introduced by the predecessor's delta: a repeated counter-changing refusal at the same site ends the demand with an error instead of retrying forever. The service does not model it; an engine answering an export or import with it would appear as `failed`. Fine for the demo; the service's policy should know the case.
7. **Per-client module load steps still run on a cold engine**: the `moduleSource` and `asModule` resolvers and one type-definition run when the definition misses. With the definition cached the load is about 0.3 s and downloads nothing; this is the floor unless `asModule`'s per-client key is revisited, which touches module loading semantics. No action proposed.
8. **The `--eager-runtime` client option pulls the whole runtime root filesystem on a cold engine** even when every later call hits (review fix B2 of the type-definition cache). Explicit opt-in, so left as is; documented in the plan.

### 1.2 Service

1. **Upload tokens are kept per engine instance keyed by digest**, relying on the engine's single export worker (plan 7.6). Two concurrent exports from one instance would need stronger correlation. Fine for this engine; must change if the engine ever exports in parallel.
2. **Index rebuild after a service restart loses producer and root recipe digests** (plan 8.3), so a restarted service can send an engine a bundle it produced and can ask for an export it already has. Duplicate imports are harmless (plan 12.8). Fix: persist those two fields in the bundle object.
3. **No pruning of the pool, no expiry handling, no removal of expired bundles** (plan 15 and 12.9). An engine sent a bundle whose root has expired answers the import with an error and nothing else happens.
4. **Static token map stands in for authentication** (plan 8.1). Real organization mapping is next.
5. **The 8 s first-poll work allowance and the 5-attempt deferral cap are fixed constants**, chosen for the demo's shapes (plan section 19, run 4 fixes). Both should become configuration or derive from the engine's startup bound.
6. **`TestNamespaceLive` is the only test against the real tenant**, one blob and one bundle under `remote-cache-demo/`. No test covers a full engine export and import through Namespace Artifacts on this host; that path is proven only by the Namespace runs.

### 1.3 Harness, host and process

1. **The end-to-end loop cannot exercise the service's retry-later path** (503 while imports are being prepared), because moto resolves every bundle within the poll's hold. The unit tests cover it; only the Namespace runs show it live. A fault-injection flag on the test store would close the gap; declined before the demo for time.
2. **The dagger.io CI cannot build the service module**, which needs the bind-mounted Dagger checkout (plan 12.5). The loop is local only until the engine changes are published.
3. **Two workstreams' CLIs removed each other's engine containers** on this host (plan section 19, 17 September). This workstream now runs on a dedicated container `remote-cache-engine`; the cause of two unexplained kills was never attributed. Docker events are being logged to `/tmp/docker-events-attribution.log`.
4. **Nested engine logs are only complete when captured from the engine itself.** The outer CLI drops nested engines' stdout under load; the harness tees each engine's stderr to a volume and saves it (dagger.io `baecd1f61`). Any new test must read evidence from those files, not the outer log.
5. **Store tests need the unprivileged in-place store.** On the original base the `engine/snapshots` store tests skipped both on the host and inside the engine test container, and summaries counted skips as passes; the predecessor's in-place store fixed it, and the rule since is per-test `--- PASS` lines only. The predecessor adopted our helper fix as `ed7a4a47f9`; the next rebase takes it in place of our `c96012aad7`.

### 1.4 Namespace track and the Dagger Cloud API (from the coordinator-namespace agent, 18 September)

1. **Dev-only API changes are on an unpushed branch** (`namespace-prep-d3a06461` in dagger.io): cache URL and token passthrough scoped by `ENGINE_REMOTE_CACHE_ORGS`, startup-wait passthrough, `ENGINE_DEV_IMAGE_ADDR` bypassing the image allow-list, `CHECK_NAME_ALLOWLIST`, `rerunChecks` accepting the org API token, the consolidator counting inactivity from the last disconnect, local telemetry honoring `PORT`. None is production-shaped; each needs a real design before landing in dagger.io main.
2. **Clean-slate engine lifecycle**: the engine of a clean-slate rerun is destroyed about 15 s after its check, cutting its own export mid-upload every run. Either such engines should not export, or the API should hold destruction while an export is in flight; the API has no signal for the latter today. **Decision.** (Same item as 1.1.2, from the API side.)
3. **Engine suspend window in production**: main suspends an engine within about a minute of its last client disconnect, so a post-session export has under two minutes plus a 60 s SIGTERM grace; the demo used a 15-minute dev override. Production needs an export-aware policy or an export that survives suspend. **Decision.**
4. **The internal module-loading org's engines are excluded from the cache** by the dev API's org scope, so every push pays a cold workspace load, about 1m30s on the dagger repo. Whether they should participate, and in which org's pool, is undecided. **Decision.**
5. **Provisioning cost**: about 18 s from clean-slate request to a running engine, plus the 10 s import gate, is fixed overhead on B before any check work; the gate shrinks only if materialization is faster or bundles smaller.
6. **Tenant token for a deployed service**: the service fetches it at startup with background refresh, but the process must start with a live AWS SSO session on this machine; a long-lived credential path for a deployed service is undesigned.
7. **Capture gaps**: B's post-check cache snapshot depends on racing the 15 s destroy window, and engine logs are fetched after the fact. A deployed setup wants engine logs and cache snapshots shipped, not fetched.
8. **Image publishing**: `hack/publish-dev-engine` pushes to a personal Docker Hub repository and the API override points at it; the engine's version string does not identify a publish. A real dev-image path through Namespace's registry is undesigned.
9. **The heavy `cli:release-dry-run` check** (7m24s cold, 1.87 GB export) has not been rerun since the imported-directory guard fix; run 3b's B failure is presumed fixed by the predecessor's `1bfece3b77`, which the rebased branch carries, but not shown on Namespace.
10. **The Namespace results document is uncommitted** in that agent's worktree (`hack/designs/remote-cache/namespace-demo-results.md`); the runbook `api/hack/NAMESPACE-DEMO.md` lives on the prep branch, with secrets in an untracked env file on this machine only.

## 2. Things to look into

1. **Why was the artifact download from Namespace Artifacts so slow?** Run 5: 25.8 MB in about 8 s, roughly 3 MB/s, for a transfer that should be Namespace-local. **Investigate**, not yet routed. What the data must answer first: whether the 8 s is the transfer at all, or the sum of resolve (a signed URL per layer, obtained at bundle rewrite), the engine's per-layer fetch order, and unpack. The engine fetches a chain's layers through one provider; whether it fetches serially is not known. Measure with per-layer timestamps from B's engine log (the `installed part` line carries totals only today; a per-layer line or a span with bytes and duration is the cheap addition) and the service's resolve timings, on one run with a larger artifact. Then decide between parallel layer fetch, larger read buffers, or a Namespace-side answer.
2. **Workspace arguments invalidate the cache by design, and that is what the demo shape now pays for.** Run 5 (Namespace results section 4): every constructor that takes the Workspace runs on B, each in its own runtime container, about 7 s; and a module object that carries the Workspace as a field makes its function calls uncacheable, so the check body runs on B even when everything inside it hits. Erik keeps the Workspace semantics; this is not a bug. Angles to work, all of which change how modules are written or how the engine treats Workspace-derived values, none of which is the remote cache's: (a) module authoring: constructors and check objects that do not hold the Workspace, taking the workspace on the specific functions that read it; (b) the Workspace-returned-object digest (plan's predecessor decision 24.3): a module object whose fields are all content-digested could match by content instead of being session-scoped, which is the mechanism that let `Build` hit in the small demo; (c) the runtime containers of Workspace-taking constructors: their filesystems were 6 MB each from the cache, so the cost is the runtime start, not bytes; (d) whether a Workspace-taking constructor's result could be keyed on the workspace's content digest when the constructor reads nothing from it. **Decision** on which angle first; recommend (b) and (a) together for the demo module set.
3. **Why was `GitRef.tree` not cached on engine B in the demo?** Erik noticed it in the demo trace. **Investigate**, not yet routed. Git trees are exportable and content-identified by design (the predecessor's decision 24.5: `GitRef.tree` and `GitCommit.tree` snapshots from either backend may be exported; the Git ref, commit and tree identities are among the marked transferable digests), and the demo's module source is a git ref, so a hit was expected. Candidates, in the order to check on B's engine log and both engines' cache snapshots: the tree's row was not selected for export (the first policy uploads a leaf's direct part-type dependencies only, and a `GitRef.tree` two or more edges below the leaf travels as metadata without its parts); its row was in the bundle but B's own `git` call resolved through a different `GitRef` recipe (a moving ref against a pinned commit, or the `#ref` form against `@version`), so the classes did not merge; or the row hit but its snapshot was pulled, which is not "not cached" but looks like it in the UI. The answer decides whether this is a policy refinement, an identity fix, or nothing.
4. **Module load on the dagger workspace takes about 1m30s on the internal org's engine** for every push, because the workspace has about 25 modules (Namespace results section 4.6). Not on B's path. Each module's definition is now cached across engines; whether the remaining time is the per-client resolvers times 25 or something else is unmeasured.
5. **Session reports of a module-loading engine carry thousands of schema rows.** The leaf filter drops them from selection, but the report itself (about 4,200 results per session) is still built, sent and stored. Whether to filter at the engine side or shrink the report is a policy question deferred by Erik to next week.
6. **The 3.5 MB demo download and the 64 MB run 5 download** are what remains after E12 and E13: the codegen and build layers of each module's runtime plus mounts. Whether those layers deduplicate across pushes of the same module version (they should, by digest) has not been observed across two runs with the same prefix.
7. **Zstd was adopted without a measurement** (D20). One measurement of upload time on A and download time on B, uncompressed against zstd, on the Namespace network, would confirm the trade.
8. **Definition-miss diagnostics stay in** (`module definition lookup` and `computed` log lines with the inputs' digests, engine commit `48d8f17e67`). They answered run 4; decide whether they stay at info level in production.
9. **Reselect retries as span events** (E15) hide the reselect churn from the UI; about 80 reselects in one cold check is itself a number worth understanding (why so many candidates change during acquisition on a cold engine), separately from how they are painted.

## 3. What exists on top of the stacked pull requests

Erik's open stack in `dagger/dagger`, bottom to top (`gh pr list`, 18 September): #13962 track 5 reader cancel → #13969 track 6 session resources → #14043 track 7 per-part evaluation → #14049 track 8 terminology → #14050 container part persistence → #14051 snapshot chains → #14093 deferred filesystem restoration (head `17f7dd89f4`). Plus #14106 (a test fix) and #13866 (independent peer sessions) on `main`.

Everything below is not in any pull request.

### 3.1 The predecessor's batches

The predecessor workstream's work sits above the stack's top. They packaged it as linear branches without evidence commits, in this order, each an ancestor of the next: `b7-packaging/remote-cache/transfer-foundations` → `b1-producers` → `b2-transfer` → `b4-acquisition` → `b5-offers` → `b6-sharing` → `b7-verification` (tip `46ee7035b8`, the packaged equivalent of their integrated tip `bf509625e7`; their later test-helper fix `ed7a4a47f9` packaged as `46ee7035b8`'s successor). One caution, checked on 18 September: the packaged branches share their merge base with the stack's top at `0d031c08ef`, which is older than `17f7dd89f4`, so they are not on top of #14093 as they stand; they include upstream `main` merges up to their base. Rebasing them onto #14093's head is the first packaging step and is theirs or ours to do.

What each batch is, in one line each (their design documents under `hack/designs/remote-cache/focused/` are the authority):
- transfer foundations and b1 producers: the lazy-value and retained-operation model that replaces the recorder design;
- b2 transfer: `WithExportedValues` and `ImportValues`, the bundle format, chains and offers;
- b4 acquisition: parts acquired on demand, local first, then download, then the lazy operation;
- b5 offers: offers before execution, bounded address renewal, the integration adapter;
- b6 sharing: snapshot sharing between equivalent results;
- b7 verification: the integrated proof, the unprivileged test store, the fixes since batch 6 (the not-ready candidate scan, named refusals and `ErrPartNoProgress`, key-only renewal, cloning part-acquired values, backing-snapshot owner leases), and the TLA modules.

### 3.2 Our engine work (`engine-main`, 49 commits above `bf509625e7`, 66 files, about 8,000 lines)

Placement as of 21 September: the predecessor's A1–A5 merged to main with
#14051–#14235 (main 9282dfa127, b831de5b6a); A6 is #14241. Of the E-series:
E9 is #14263 (main PR, its test rewritten against main's helpers); E12 and
E13 are #14264 (main PR, seven commits, builtin-image blob lifetime and
builtin layers from the local store); E15 merged with #14229. The rest
(E3, E2a, E2b, E1, E4–E8, E10, E14, the cached module definitions, E11) are
still on engine-main and unplaced; c96012aad7 is superseded by ed7a4a47f9
(merged).

Grouped by package as the plan names them, with the commits that make each up (all reviewed and approved; see plan section 19):
- **E3** protocol wire types: `0b1058bd97`, `58574fb475`.
- **E2a, E2b, E1** cache additions, adapter and session hook, configuration: `fecef1c4c7`, `2ddbe67d9c`, `b41b9f2f4b`, `9a1b330343`, `2dfd8b2589`.
- **E4, E5, E6** channel client, upload path, log lines: `373d91329e`, `d6491c4b50`, `fd2deaa716`, `5d16e4d17c`, `08564bb490`.
- **Cached module definitions**: `0631a80713`, `e2869e377a`, `9d9349a381`, `8624d5cf99`, `1e417aef41`, `3d7a492ae2`, `48d8f17e67`.
- **E7, E8, E10, E14** startup gate and backlog drain, first poll, Retry-After: `68c6d143a5`, `55c27dc93b`, `d61425f383`, `1e9175b05d`, `8aae39949e`, `51fd5944fc`.
- **E9** null rows record their dependencies: `3d2cbd783c`, `88304eb722`.
- **E11, E12, E13** zstd, blob lifetime, builtin layers from the local store: `535165c6a7`, `ab9db370a0`, `53db181a15`, `5193483904`, `c3139228cf`, `fcf5245821`, `54cf1fbd51`, `3c448d99d9`, `b4961caf9b`, `b347418163`, `c96012aad7` (test helper, superseded by their `ed7a4a47f9`).
- **E15** reselect retries are not span errors: `3587242fe7`.

### 3.3 The service (dagger.io, `service-seat-daggerio-8aeecb96`, 53 commits, 53 files, about 30,000 lines including generated bindings and README)

All new: the `remote-cache/` module (server, pool, policy, blobs with S3 and Namespace Artifacts backends, status types, the runner module under `.dagger/`, the loop and demo scripts, the end-to-end tests, the README). Nothing of it depends on `api/`. Last reviewed code tip `cf4266216` (one confirmation open); README tip above it.

## 4. Suggested batching into further stacked pull requests

Principles: each pull request is reviewable alone, has its own tests, and builds on the one below; the predecessor's batches go first because everything of ours builds on their APIs; our packages go in dependency order; the service is a separate repository and one or two pull requests of its own.

**Dagger repo, above #14093:**

| # | Pull request | Content | Why this cut |
| --- | --- | --- | --- |
| A1 | transfer foundations and producers | packaged `transfer-foundations` + `b1-producers` | The lazy-value model every later batch assumes. |
| A2 | value transfer | packaged `b2-transfer` | Export, import, bundle format. Reviewable as a format. |
| A3 | acquisition | packaged `b4-acquisition` | Parts on demand; the acquisition order is the contract. |
| A4 | offers and renewal | packaged `b5-offers` | Adds the integration adapter the service uses. |
| A5 | snapshot sharing | packaged `b6-sharing` | Independent mechanism; separate review. |
| A6 | integrated verification and fixes | packaged `b7-verification` + `ed7a4a47f9` | Their fixes and the unprivileged test store; last of theirs. |
| B1 | remote cache protocol and cache additions | E3, E2a, E2b, E1 | The engine's half of the wire contract and the session report; no network code yet. |
| B2 | remote cache client | E4, E5, E6, E7, E8, E10, E14 | The channel client, uploads, startup gate. One PR because the gate's tests live in the client's scripted transport. |
| B3 | null results record their dependencies | E9 | A one-block cache fix with its own test; independent of B1 and B2, could go before them. |
| B4 | cached module definitions | the seven definition commits | The largest behavior change outside the remote cache; deserves its own review. Depends on nothing in B1 to B3, only on A6's base. |
| B5 | snapshot blobs and builtin layers | E12, E13, E11 | Blob lifetime, builtin-store copies, zstd. Snapshot code only; the reviewer of A2 and A3 is the right reviewer. |
| B6 | reselect telemetry | E15 | Tiny, independent. |

B3, B4, B5 and B6 do not depend on B1 or B2; order them for reviewer convenience, not for code. The evidence commits (`continuation-evidence/…`) are not part of any pull request.

**dagger.io:**

| # | Pull request | Content |
| --- | --- | --- |
| S1 | remote cache service | `remote-cache/` server, pool, policy, blobs (S3 backend), status types, unit tests. |
| S2 | Namespace Artifacts backend and end-to-end harness | the Namespace store, the runner module, loop and demo scripts, the end-to-end tests, README. Depends on the engine changes being published somewhere the module's `replace` can point at, or on vendoring the engine packages, before dagger.io's CI can build it. |

**Before packaging anything:** rebase the predecessor's packaged branches onto #14093's head (their base is older); then rebase `engine-main` onto the result, taking `ed7a4a47f9` in place of `c96012aad7`; then run the loop and the demo once on the rebased pair. That is the same procedure the rebase track used on 18 September (plan section 19), and the two forks that did it have the context if they are kept.
