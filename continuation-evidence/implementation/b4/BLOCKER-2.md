# Blocker 2: cold module closure contains eager Containers without saved producers

The inline ownership blocker is resolved by Addendum 1. This is a separate failure of the required native cold-module proof. The acquisition implementation and scoped inline checks are present, but the commission is not complete.

**Decision received:** the coordinator accepted this record and probe and selected option A narrowly in `remote-cache-coordinator-dagger-successor-5707b13a:continuation-evidence/implementation-reviews/b4/BLOCKER-DECISION-2.md` (read in full). Option B below is historical and was rejected as the primary proof route. Implementation awaits the promised council-confirmed Batch 1 and Batch 4 Addendum 2 commits. Meanwhile the independent mixed actual exec and boundary work has been completed; see [REPORT.md](REPORT.md). No recipe has been synthesized from a recorded field and the cold test has not changed.

## First observed divergence

Run the unskipped `TestRemoteCacheTransferSuite/TestSchemaRecoveryCold` with two real dev engines and separate state, clients and module directories. A executes the ordinary report, including the selected Directory artifact. The gated fixture exports the saved report closure and that artifact's actual chain, copies the bundle and blob files, and imports them into B. B has made no `AsModule`, SDK runtime warmup or `Serve` call before import.

B's subsequent ordinary `ModuleSource(".").AsModule().Serve(ctx)` fails before the report selection:

```text
failed to call module "cache-probe" to get functions: call constructor:
imported filesystem part is unavailable: {"part":"mount:/schema.json"}
```

[Failure excerpt](logs/cold-module-excerpt.log). The run completed with exit status 1; it was not a mount-privilege skip or a timeout. Trace: <https://dagger.cloud/dagger/traces/3d2311232b262d9242880d88c6437624>.

[Actual A closure projection](probes/cold-closure-summary.json) has 226 values and only ordinal 2's Directory snapshot selected for transfer. The relevant rows are:

| Ordinal | Recorded field | Saved producer | Relevant normalized part |
| --- | --- | --- | --- |
| 199 | Host `directory` | none | Directory `transfer_pending` |
| 206 | Directory `withoutFile` | completed, JSON present | Directory `transfer_pending` |
| 212 | Container `withMountedFile` | none, no JSON | `mount:/schema.json` pending |
| 213 | Container `withMountedDirectory` | none, no JSON | `mount:/src` and `mount:/schema.json` pending |
| 214 | Container `withWorkdir` | none, no JSON | both mounts pending |
| 215 | Container `withEnvVariable` | none, no JSON | both mounts pending |
| 216 | Container `withoutDefaultArgs` | none, no JSON | both mounts pending |
| 217–218 | Container `withExec` | completed, JSON present | both mounts pending |

The earlier cold run surfaced `mount:/src`; the final run surfaced `mount:/schema.json`. The eager Container chain has both mounts and FS pending, with no pending offers. The final bundle also confirms the report artifact is now a declared `result_id` reference to ordinal 2, after the separately fixed typed-field relocation bug. The final warmed control passes both import orders and its restart/default checks. No missing recipe was synthesized from their recorded field. The observed failure alone does not establish that adding these recipes would make the entire proof pass; exact Host source availability must also be checked.

## Parent reproduction and source

The [probe](probes/eager_container_producer_test.go.txt) ran in a detached worktree at the exact unchanged parent `77f6279559061fd1bb6b3b18e6b08582c7b013a3`. It invokes the real installed schema's eager `container(platform:"linux/amd64").withWorkdir(path:"/src")`, captures the result and exports it. [Output](logs/parent-eager-producer.log) confirms no `lazyJSON` and foreign `producerState:"none"`. This is a passing assertion of the existing gap, not an acquisition acceptance test. It deliberately needs no snapshot manager or engine execution; the mounted-output evidence comes from the real cold run above.

The parent code explains both observations:

- `core/schema/container.go`, `cloneContainerForSchemaChild`: when the parent is complete, it clones configuration and accessors into a new Container without a live or completed recipe.
- The `withMountedFile` and `withMountedDirectory` eager branches call the eager core methods and return without saving their existing typed recipes. Their lazy branches record those recipes.
- `core/container.go`, `WithMountedDirectory`: evaluates its Directory input, clones the completed snapshot into a mount accessor, and returns. It records no producer.
- Metadata transformations such as `withWorkdir` similarly save their typed recipe only for a pending parent.
- Integration decision 1 attaches an **existing** completed Container recipe at publication. It cannot attach a recipe absent from the value.

## Contract boundary and decision options

Batch 4 §3/§5 acquires through a local equivalent, an admitted offered chain, or a saved producer. It must return unavailable when none exists. Batch 1 §1 limits its new eager producer coverage to the four Git producers, HTTP File, builtin Container and generated schema File; its generic recorder is File/Directory-only. A recorded field alone is not an executable producer. No parent commits or foundation scope have been changed to bypass this boundary.

**A — extend explicit eager producer coverage for the required Container schema paths.** Specify and record the existing typed recipes on exclusively owned completed children, retaining their exact parent/source inputs and publication attachment. Keep ordinary eager execution unchanged. Audit the required closure, including the Host/source input availability, then repeat the cold proof. This foundation extension uses typed producer recipes and should land as new commits above the parent; generic invocation of recorded call frames remains out of scope.

**B — keep those outputs producer-less and specify the missing transfer selection.** Carry selected chains for the required non-reproducible closure outputs through the existing `ValueSelection.Outputs`/offer path. State which source/mount outputs the cold fixture must include, while leaving builtin FS to the required local-equivalent or saved-builtin route. This uses the existing mechanism but changes the proof's source-availability assumptions; exporting every FS indiscriminately would hide the builtin requirement.

Neither option is implemented here. The cold test remains enabled and fails at the recorded boundary. The report distinguishes passing focused checks from unfinished cold and mixed-exec acceptance work. No author or reviewer was contacted.
