# Batch 4 implementation report

**Status: the binding Addendum 2 implementation and focused verification are complete; the commission is blocked by a newly reached scratch Directory gap.** The cold run now passes ordinary AsModule/Serve, the matching report hit and selected artifact bytes, both saved mount writers, all 13 delegated installations and the SDK-base/Host route assertions. Its existing changed-argument control then fails on the imported canonical scratch Directory, which has no saved producer, offered chain or eligible B result. [BLOCKER-3.md](BLOCKER-3.md) records the precise failure, real-store probe and two decision options. No scratch behavior or cold-test setup was changed to hide it.

## Inputs and commit boundary

- Parent: `77f6279559061fd1bb6b3b18e6b08582c7b013a3`, reset as requested before implementation; its commits are unchanged.
- Commission: `10b43c933bce4afe4ee273332c79e15bcbd979c4:continuation-evidence/implementation-commissions/BATCH4-ACQUISITION-IMPL.md`.
- Base acquisition design: `2ad7bff7a4f61e39361fd222230a21bf4ffb3569`, blob `b463258caa499307b5a44032664bae37aba11623`.
- Binding Addendum 1: `04f6d695d4a944fd27e98eb006ed0cda761b3dca`, blob `289ba0f892f64eabcb01718f0f65fe1e21fb6d7f`; both consolidated council reviews and blocker decision 1 were read.
- Binding producer Addendum 2 final: `d674bea3d6f270c8fe3b2118b87bda754e26a89e`, blob `3e254e4d36a9cd80848146b223067f84d2c98ad3`.
- Binding acquisition Addendum 2 final: `7af104b49aad9dd4c9a02b42b9d2152e59a9c8fe`, blob `d63729442180a2d7faf872b687bf8d64cf19083b`. The earlier confirmed texts `0fae70578e` and `3ee2e8d1ba`, the full council consolidation `a8c33ab8e73ae64a37a07042a6e1a0adf0a4211d`, and both editorial diffs were read. The editorial changes add no rule.
- Producer design `860cc5c8b6` §10 and Addendum 1 `8b8181c2cc`; transfer design `2e75801259` §§4, 4.2, 7 and addenda; explanation blob `54dcdac7` §§10.4, 12, 13, 18; shared resolutions A–D, F, H, I, L at `10b43c933bce`; integration decisions 1/2 at `ba6f7cfcdc8e`.
- The complete `skills/engine-debugging/SKILL.md` was read before choosing checks. The real-store tests used the privileged private mount namespace runner and executed their storage assertions.

The implementation order is preserved. `21234ff019` is the historical blocker-1 evidence commit between steps 1 and 2; it was not rewritten. Every change to foundation-owned files is a new commit above the specified parent. The final separate evidence commit contains this report.

| Step | Commit | Contents |
| --- | --- | --- |
| 1 | `e307838a698b7845e0bf743225281d90b01b01e1` | Writer gate, stable host, pure routing, supplied lazy task and continuations |
| 2 | `1ccf619f43d0710b1eefe92fec238ade8397eaaf` | Source ranking/D1, independent pin, root Ready preparation/publication/receipts |
| 3 | `f19717d0a067d8f33c18fc94695dabc2bceb76ff` | Actual selected-chain import, content classification, exhaustion and D2 |
| 4 | `df39d040c56f31cce8edf47855d205bb9dcfe909` | Fresh private producer, output postconditions and mixed raw Container restore |
| 5 | 521b90d51d0ac66771aea0da460549e6b0592e63 | Scoped inline Addendum implementation, focused matrix, selected-byte fixture and enabled native proof |

All implementation commits are buildable and signed off. Step 5's acceptance remains limited by the cold result below; commit presence is not a claim that the full commission passed.

Subsequent signed commits preserve the reviewed history:

| Commit | Follow-up |
| --- | --- |
| `8e0e2198d8` | Remove the bundled object-field fix in preparation for isolated review; compile check passes |
| `5b73480d43` | Reapply only the declared SDK object-field retention fix and its regression, as requested |
| `506649d04a` | Real two-engine mixed downloaded FS/private execMeta proof and gated release observation |
| `2777bb534d` | Decision, cancellation, decode/publication, tuple coherence and retained-pin boundary cases |
| `c4d58da56b` | Whole-producer restart, selective pending image metadata, native recipe guards and Ready donor backreference release |

New signed commits for the binding Addendum 2 dispatch:

| Commit | Change |
| --- | --- |
| `f134133fc8` | Record the two completed eager mount recipes; preserve exact inputs and replay fresh recipes |
| `c02411efad` | Closed exact-parent delegation, private proof/selector/path, gated observations and local pending restore |
| `21d0da8c86` | Actual resolver rejection/cleanup checks, including borrowed-input preservation |
| `3d90f1f724` | Release shadowed parent mount clones on eager construction failure |
| `ff0879dfcb` | Source-ordering/waiter tests, exact proof checks, shifted roles, A→B→C, native restart and depth costs |
| `3337963558` | Cold exact-row/full-address SDK and Host counters, separated from producer entries |
| `4ccf106dd4` | Report all inherited mount-producer routes with exact row/address and recorded parent |

The final separate evidence commit is listed in the delivery reply. No reviewed commit or parent commit was amended.

## Implemented behavior

The acquisition collector inspects copied records without decoding or opening storage, checks the owner's completed output first, then ranks all ordinarily eligible equivalents Ready before chain before saved producer, with stable route/ID ordering. Ordinary lookup/e-graph selection code is unchanged. D1 checks offer-owner dependencies' own resource requirements with the foreground session. The private sessionless Ready constructor accepts only imported receivers and current own-requirement subsets, with revalidation at Commit.

Prepare owns an independent snapshot pin, exact output/reference holds, complete next record and typed accessor preparation. Commit validates generation, gate, representation revision, source authority, roles and dependency cycles before the E→G→core-guard→P store. Ready sources stay held through Commit; admitted chains retain copied offer authority independently of source row or slot. Publication hands off offer/donor ownership before bookkeeping. Finish consumes its bounded receipt hold, supports the owner-sync barrier, and continues cleanup → owner sync → pin release → D2 without rerunning a body/download. Sync-pending continuation state has no self-hold or retained offer owner.

Actual `ImportChain` receives fixed-address or in-process providers. Only supplied chain/content failures become `ChainContentError`; writer/lease/local storage and cancellation retain their ordinary classification. Demand exhaustion uses source identity, full address, value and ordered content, so refreshing an address does not retry the same content. Private producer attempts decode fresh recipes and accessors; they validate the demanded output and complete write set, preserve previously final outputs, and release redundant private outputs. Container raw metadata/parts/roles/producer data live in one immutable versioned view independently of typed producer latches.

Addendum 1 is implemented in dependency order: path/key/SQL helpers, link preservation, visitor/capture projections, decode carrier and core loader guards, guarded typed collection, scoped ownership and boot restoration, then representation/race/real-store cases. Inline items retain the enclosing row and full path; a `result_ref` changes owner. Snapshot link copies use the one internal `cloneSnapshotRefLinks` helper (core calls its facade). Duplicate `(result_id, output_path, role)` inserts are checkpoint errors. The desired-set stale-lease scan runs at every cache boot, including no persisted rows and after `import_failure` reset; a failed scan fails initialization.

`InstalledOutputs` is declared. `PartDemandState` leaves room for batch 5's second keyed set. Prepare/Commit/Finish arity is unchanged; no batch 6 ordered-base or predecessor provenance fields were implemented.

Addendum 2 records the original typed parent/source, resolved target, effective owner and readonly option at the two eager mount resolvers. The narrow recorder checks construction ownership and completed accessors without evaluating or loading anything, then stores only `completedRecipe`. The parent's existing completed-recipe attachment fallback is retained and tested. Failure cleanup covers the fresh child and mount clones shadowed by the body, uses an uncanceled context and preserves cleanup errors.

Delegation uses the exact receiver dependency for the ten closed core fields, with no saved recipe and consumed metadata. It preserves child metadata and translates mount target/kind to the child's own positional role. The private selector keeps own final outputs and ordinary Ready ties first, admits a Ready parent before a chain, and demands a pending parent only after the existing routes. Public equivalent-source selection keeps its prior meaning. A copied context path detects repeated row/full-address pairs before task joins. The child holds no write permit while awaiting the parent, and an already waiting caller waits for parent completion even when the child independently installs first.

The delegated source proof binds both exact registrations, frozen frames, receiver edge/mapping, representation and descriptor versions, requesting-session resources and full addresses. Normal Prepare/Commit/Finish supplies independent pins, accessor refs, child roles, cycle checks and owner sync. The temporary parent hold drops at Commit/refusal before Finish or sync-failure retention. Never-imported pending metadata children decode into the same managed raw representation only when the closed mapping is valid; unsupported native missing-recipe states still fail.

The fixture's single new `Source` field carries parent ID/full address only for `selected-delegation` and `installed-delegation`. The cold test reports these in its acquisition counters and reports mount producer entries separately. No production event collection is enabled by default, and no producer is inferred from a field name.

## Ordinary execution changes

These are the changed paths and their intended effects:

1. `dagql/cache.go` and `cache_part_task.go`: the shared lazy kernel gains supplied bodies, generation and continuation state; native callbacks retain ordinary retirement, cancellation and bookkeeping semantics. Synthetic completion does not set native whole-result completion.
2. `core/file.go`, `directory.go`, `container.go`, `container_parts.go` and `filesystem_output.go`: attached callbacks enter a stable host/gate before body latches. Detached execution remains direct. File/Directory complete tuples are guarded; raw Container readers see one version. Opening an acquired output follows demand before reading its descriptor. An unused lazy host allocates no gate/task/provider state.
3. `core/lazy_state.go`: native completion, including failed bodies that may have mutated output state, increments an atomic output revision. Guarded capture and typed role collection retry on concurrent changes. Container revision remains available through retained completed recipes after the operational pointer clears.
4. Persistence/encoding/visitors: snapshot links carry canonical declared paths; list encoding, capture, decode and typed role collection preserve them. Inline decoders have result ID zero plus a distinct owning-row/path carrier; copied empty maps are authoritative and mismatched carriers fail. Composite cleanup covers inline codec values and CAS losers. A public `NthValue` child borrowing an inline object retains its enclosing row and does not duplicate accessor cleanup or rebind its host.
5. Persistence/boot: all root and inline lease IDs use the scoped key. Existing root leases are re-keyed by attaching the full desired set before stale removal. Private schema 20 gains the path column/stricter key; envelope 4 and bundle 1 are unchanged. Old private-20 stores lacking the column follow `import_failure` reset, then empty desired-set reconciliation. Applied, desired and attempted role maps all use `(path, role)`.
6. `core/object.go`: **outside the acquisition design; requires council judgment.** Declared object/interface fields received from an SDK now keep the already attached result, including string-handle and object-map inputs. Previously attachment retained the dependency but left the raw SDK value in the field; a handle string then serialized as opaque scalar JSON and escaped relocation. This uses the existing typed reference grammar and SDK conversion path. It changes internal stored representation while preserving SDK-visible values; opaque scalar fields remain scalars. See the isolated-fix justification below.
7. Snapshot import adds an independent pin and optional chain-content annotation mode. Ordinary image import keeps its previous mode and error behavior; a real regression checks shared chain reuse.
8. The environment-gated transfer fixture accepts selected Directory/File snapshot IDs and Container FS IDs, carries real blob files, binds an in-process provider, and records selected/installed routes, provider reads, producer entry, sync and settlement. Its report includes copied applied snapshot links. With observation enabled, a private Container's actual FS ref is wrapped after execution to report successful or failed `Release` after the underlying call returns; the wrapper neither replaces the snapshot manager nor simulates execution. The unconfigured engine has no fixture schema field or event collection, and private refs are not wrapped. The probe module returns and reads a selected Directory artifact; the cold test remains enabled; this dispatch adds route assertions without changing its setup, export selection, cold ordering or existing controls.

9. `core/schema/container.go` and `core/completed_producer.go`: the two eager mount calls now retain completed recipes. They remain eager with the same values/options and borrowed inputs. Their new invariant failure path releases construction-owned refs, including shadowed clones; owner/body failure cleanup also covers the successfully cloned child. Tracking the original mount list adds a shallow slice copy to those construction paths.
10. `core/part_routes.go`, `dagql/cache_part_delegation.go` and the demand/Commit paths: eligible missing parts can acquire from the exact recorded parent, with the specified ordering and waiter latency. This affects managed missing-part demands, not ordinary request/egraph lookup.
11. `core/container.go` and `dagql/cache_persistence_import.go`: a never-imported locally persisted pending metadata child with a valid mapping is now admitted for foreground acquisition after decode. Boot only marks the pure mapping and does no producer/provider work; ordinary completed local restore remains on its existing path.

## Isolated object-field fix for council review

The failing real case was the warmed module report's declared Directory artifact. After transfer, ordinary `artifact.file(path:"payload.txt").contents` followed an A-engine handle left in scalar JSON and selected an unrelated B Directory. Both warm orders failed before this fix; [the failure excerpt](logs/warm-before-fix-excerpt.log) and [the before regression](logs/declared-handle-before.log) establish the defect. The focused regression expected `result_id` but received `scalar_json`. After the fix, the same field enters `VisitEncodedReferences`, changes to the relocated ID, and preserves ordinary SDK conversion and dependency lifetime. Both warmed engine orders pass, including artifact contents and restart.

The fix was initially bundled in `521b90d51d`. To honor the new request without rewriting reviewed history, `8e0e2198d8` removes exactly that change and `5b73480d43` reapplies it alone. The inverse commit compiles; the isolated fix passes the same focused object/relocation race selection ([output](logs/object-isolated-race.log)).

The narrower dependency-only approach already existed and is exactly what failed: ownership alone cannot relocate a field represented as an opaque scalar. Parsing arbitrary strings as handles in generic persistence would reinterpret legitimate scalar strings; changing only the gated module fixture would conceal the real SDK boundary defect. Retaining the result at the existing declared Object/Interface conversion point uses type information and the established reference grammar, including nested object-map inputs. No generic scalar conversion changes. These facts justify the chosen scope but do not substitute for council approval of this additional behavior.

## Verification and limits

Exact commands and outputs are in [COMMANDS.md](COMMANDS.md). Focused race checks cover:

- Own/Ready/later-route ranking, ordinary lookup control, D1 admission, sessionless subset refusal/revalidation, source expiry, canceled Commit/Finish, task isolation/continuation/NoJoin and finite decision drain.
- Admitted chain survival after donor collection and slot replacement, offer-only back-reference release after sync failure, installed-output cycle rejection before mutation, and D2 retirement of replacement slots.
- Decode after encoded Commit and before external Finish, copied empty/mismatched carriers, concurrent typed publication, scoped visitor rollback/validation and SQL conflicts.
- Real root Ready/chain/private File routes, content failure then producer fallback, partial owner-sync retry with no repeated body/read, installed-but-unsynced checkpoint/restart, target-to-receiver mount role mapping, known metadata with no layer reads, mixed Container chain restart/re-export, and fresh whole builtin invocation.
- Two inline outputs at distinct paths (including the same snapshot at both paths), independent result-ref child, concurrent publication, ordinary Nth borrowing across sessions, exact-once final accessor cleanup, clean restart and selected re-export. Nested nullable/list representation and concurrent typed collector retries are covered separately.
- Real content missing/truncation/checksum/apply failure versus writer/lease/cancellation, retained prefix, canceled waiter, native image reuse and independent pin ownership.
- The required pre-existing native retirement/cancellation/bookkeeping/group-concurrency and core direct/refined/unrefined routing regressions.

The final dispatch's exact sequential commands and outputs are in [COMMANDS-ADDENDUM2.md](COMMANDS-ADDENDUM2.md); [COMMANDS.md](COMMANDS.md) retains the earlier command history. All 17 implementation package-level commands passed, including every previously passing implementation package selection named there, all original native kernel/routing regressions, scoped inline/boot cases, real snapshot fault controls and the new recorder/delegation checks. No real-store check skipped. The historical parent gap probe also passed again on the unchanged exact parent. The warm schema-recovery engine selection passed (377.405 s invocation), and the real mixed downloaded-fs/private-execMeta selection passed (111.400 s invocation). [Engine manifest](validation-addendum2-engines.json), [warm output](logs/addendum2-warm-engine.log), [mixed output](logs/addendum2-mixed-engine.log).

**Cold remains FAIL at a new boundary.** The binding mount/delegation changes pass their native route assertions: 13 exact delegated installations, one write each for the schema File and source Directory mounts, both cache and system-env fs hops, a builtin route, and a matching B Host capture. Matching report body count is zero and selected artifact bytes are read. The changed-argument report then fails on `Query.directory` scratch acquisition. The real-store probe reproduces the gap even with B's canonical scratch snapshot already available; an ordinary warm Directory row makes it succeed. [Blocker and options](BLOCKER-3.md), [cold output](logs/cold-addendum2-excerpt.log), [actual closure projection](probes/cold-addendum2-closure-summary.json), [probe](logs/scratch-gap-probe.log). Later cold changed-argument count, restart and future-default controls are not claimed to pass.

The final cold rerun confirms the same boundary (153.45 s selected test; 195.036 s invocation). Its [exact route output](logs/cold-addendum2-final-excerpt.log) additionally reports the mount producers' inherited fs and schema-mount routes. It records 14 delegation selections and 13 unique installations; selection can repeat, while each installed row/full-address is asserted once. Its foreign-context subtest passes independently. The earlier closure projection and row IDs remain labelled as the first run's evidence; row IDs are not assumed stable across runs.

The original warm proof and mixed actual exec proof remain documented in the historical [warm output](logs/warm-module.log), [mixed output](logs/mixed-exec-engine.log) and [mixed measurement](logs/mixed-exec-measurement.log). The mixed test uses two real dev engines, downloads FS independently, privately executes execMeta once, preserves the installed FS, releases the distinct redundant private FS before sync, and does not rerun on repeat reads. Current-dispatch reruns are listed in the new command ledger.

The remaining independent boundary follow-up passes focused race selections: actual late Ready and chain arrival during private preparation with zero private runs; a second stale preparation releases both real pins and returns reselect; ordinary and NoJoin conflicts during an inline decision; sibling admission before that decision's sync finishes; a late offer cannot interrupt Running; cancellation during pin preparation prevents publication and balances ownership; a broken local Ready descriptor returns its storage error. A decode paused on an old representation loses to a real new installation, releases the temporary value once, and sees the winning complete roles. File and Directory tuple publication races cover path, platform, service and snapshot coherence plus guarded role readers. A failed retained-pin release retries without downloading again or repeating successful owner sync. [DagQL output](logs/boundary-dagql.log), [core output](logs/boundary-core.log).

The whole-producer restart case uses a real saved `_builtinContainer` recipe and a controlled valid mixed representation with FS transferred and execMeta pending. It checkpoints/reopens, performs no provider or producer work at boot, then invokes a fresh whole builtin, preserving the first installed FS and the raw recipe. The builtin resolves execMeta to **absent** and releases its redundant FS. This is a representation/whole-invoker proof, not a claim that builtins produce exec metadata snapshots; the real mixed exec case above proves the non-absent variant. Native pending Containers without a recipe still error outside the imported adapter or the newly authorized exact-parent mapping. Pending image metadata also runs only its metadata producer and leaves FS pending with no snapshot open or provider read. [Whole/native output](logs/whole-restart.log), [pending metadata output](logs/pending-image-metadata.log).

A Ready donor with a direct donor→receiver dependency collects before external Finish opens the owner's sync barrier. The published receiver does not gain a donor edge, and it collects after its final session and persisted owners release. [Backreference output](logs/ready-backreference.log).

New focused tests additionally pass all ten pure mappings and unsupported/malformed frame controls; Ready-parent versus child-chain ranking; ordinary Ready ties; chain-before-pending-parent fallback; a paused parent with independent child installation and no early waiter return; parent-hold release with the child pin retained after partial sync failure; copied path cycle/sibling controls; surviving File mount role `mount_file:1` → `mount_file:0`; wrong-kind refusal; A→B→C relocation without local links; and never-imported local pending restart at depths 1, 8 and 32. Exact tests and artifacts are linked in the new ledger. The cold changed-argument scratch gap prevents commission completion. No batch 5 renewal, batch 6 sharing worker, scratch producer or intrinsic-scratch rule was added.

## Runtime measurements

| Measurement | Current dispatch sample |
| --- | --- |
| Root lease re-key, 1 role | 5.37 ms; 1 attach, 1 stale scan, 1 removal; peak 2 owner leases |
| Root lease re-key, 32 roles | 135.15 ms; 32 attaches, 1 stale scan, 32 removals; peak 64 owner leases |
| Typed collector, 1 output / depth 1 | 2.93 µs, 1,794 B, 17 allocations per scan |
| Typed collector, 128 outputs / depth 1 | 316.75 µs, 133,205 B, 1,185 allocations per scan |
| Typed collector, 512 outputs / depth 3 | 2.07 ms, 882,868 B, 5,446 allocations per scan |
| Concurrent publication, 128 outputs | 156 attempts / 28 retries / 128 accepted; 41.27 ms |
| Concurrent publication, 512 outputs | 134 attempts / 6 retries / 128 accepted; 271.40 ms |
| Native callback baseline | 0.66 ns, zero bytes/allocations per entry |
| Activated native host entry | 1.70 µs, 504 B, 13 allocations per entry |
| Ready File acquisition | 8.74 ms; 1 pin, 1 sync attempt, 0 private bodies |
| Chain File acquisition | 45.80 ms; 1 pin, 1 sync attempt, 0 private bodies |
| Partial-sync retry | 64.53 ms; 1 pin, 2 sync attempts, no repeated download/body |
| Failed content → private File producer | 50.53 ms; 1 pin, 1 sync, 1 private body |
| Private File producer | 42.36 ms; 1 pin, 1 sync, 1 private body |
| Native restored delegation, depth 1 | 8.27 ms; 1 independent accessor ref, pin and child owner-sync step |
| Native restored delegation, depth 8 | 70.22 ms; 8 independent accessor refs, pins and child owner-sync steps |
| Native restored delegation, depth 32 | 291.18 ms; 32 independent accessor refs, pins and child owner-sync steps |

[Collector/host samples](logs/addendum2-collector-costs.log), [real route/re-key samples](logs/addendum2-route-costs.log), [depth costs](logs/addendum2-delegation-costs.log). Delegation creates one independently owned installed view and durable role per hop, with zero producer bodies/downloads in these Ready-ancestor samples; temporary pins release after owner sync, while accessors and owner leases remain with their rows. The existing receiver edges remain. These measurements include capture/probe and graph work, so they are not a constant-time per-hop guarantee.

These are Linux/amd64 (AMD EPYC 9554P, Go 1.26.6) samples, not throughput bounds. The collector race performs 8,192 publications and requires 128 complete accepted scans; retries depend on scheduling. The native host microbenchmark excludes bodies/storage. The chain-restart sample takes 66.47 ms, with counters reset on reopen, so it is not a whole-attempt pin total. Private execution allocation totals and large-egraph scaling remain unmeasured. The earlier real-engine mixed execMeta demand measured 155.39 ms; that historical timing is not presented as a new sample.

No pushes, pull requests, tags, or author/reviewer contacts were made. Evidence contains focused commands, diagnostic excerpts, probes and measurements.
