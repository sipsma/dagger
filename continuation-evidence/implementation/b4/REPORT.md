# Batch 4 implementation report

**Status: blocked on the native cold-module proof; the commission is incomplete.** The five implementation commits contain root and scoped inline acquisition. The first inline-ownership blocker is resolved by Addendum 1. The complete commission cannot be declared passed until the native cold proof and remaining acceptance cases pass. [BLOCKER-2.md](BLOCKER-2.md) records the observed cold closure's missing eager Container producers and two decision options.

## Inputs and commit boundary

- Parent: `77f6279559061fd1bb6b3b18e6b08582c7b013a3`, reset as requested before implementation; its commits are unchanged.
- Commission: `10b43c933bce4afe4ee273332c79e15bcbd979c4:continuation-evidence/implementation-commissions/BATCH4-ACQUISITION-IMPL.md`.
- Base acquisition design: `2ad7bff7a4f61e39361fd222230a21bf4ffb3569`, blob `b463258caa499307b5a44032664bae37aba11623`.
- Binding Addendum 1: `04f6d695d4a944fd27e98eb006ed0cda761b3dca`, blob `289ba0f892f64eabcb01718f0f65fe1e21fb6d7f`; both consolidated council reviews and blocker decision 1 were read.
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

All implementation commits are buildable and signed off. Step 5's acceptance status is limited by the native results below; commit presence is not a claim that the full matrix passed.

## Implemented behavior

The acquisition collector inspects copied records without decoding or opening storage, checks the owner's completed output first, then ranks all ordinarily eligible equivalents Ready before chain before saved producer, with stable route/ID ordering. Ordinary lookup/e-graph selection code is unchanged. D1 checks offer-owner dependencies' own resource requirements with the foreground session. The private sessionless Ready constructor accepts only imported receivers and current own-requirement subsets, with revalidation at Commit.

Prepare owns an independent snapshot pin, exact output/reference holds, complete next record and typed accessor preparation. Commit validates generation, gate, representation revision, source authority, roles and dependency cycles before the E→G→core-guard→P store. Ready sources stay held through Commit; admitted chains retain copied offer authority independently of source row or slot. Publication hands off offer/donor ownership before bookkeeping. Finish consumes its bounded receipt hold, supports the owner-sync barrier, and continues cleanup → owner sync → pin release → D2 without rerunning a body/download. Sync-pending continuation state has no self-hold or retained offer owner.

Actual `ImportChain` receives fixed-address or in-process providers. Only supplied chain/content failures become `ChainContentError`; writer/lease/local storage and cancellation retain their ordinary classification. Demand exhaustion uses source identity, full address, value and ordered content, so refreshing an address does not retry the same content. Private producer attempts decode fresh recipes and accessors; they validate the demanded output and complete write set, preserve previously final outputs, and release redundant private outputs. Container raw metadata/parts/roles/producer data live in one immutable versioned view independently of typed producer latches.

Addendum 1 is implemented in dependency order: path/key/SQL helpers, link preservation, visitor/capture projections, decode carrier and core loader guards, guarded typed collection, scoped ownership and boot restoration, then representation/race/real-store cases. Inline items retain the enclosing row and full path; a `result_ref` changes owner. Snapshot link copies use the one internal `cloneSnapshotRefLinks` helper (core calls its facade). Duplicate `(result_id, output_path, role)` inserts are checkpoint errors. The desired-set stale-lease scan runs at every cache boot, including no persisted rows and after `import_failure` reset; a failed scan fails initialization.

`InstalledOutputs` is declared. `PartDemandState` leaves room for batch 5's second keyed set. Prepare/Commit/Finish arity is unchanged; no batch 6 ordered-base or predecessor provenance fields were implemented.

## Ordinary execution changes

These are the changed paths and their intended effects:

1. `dagql/cache.go` and `cache_part_task.go`: the shared lazy kernel gains supplied bodies, generation and continuation state; native callbacks retain ordinary retirement, cancellation and bookkeeping semantics. Synthetic completion does not set native whole-result completion.
2. `core/file.go`, `directory.go`, `container.go`, `container_parts.go` and `filesystem_output.go`: attached callbacks enter a stable host/gate before body latches. Detached execution remains direct. File/Directory complete tuples are guarded; raw Container readers see one version. Opening an acquired output follows demand before reading its descriptor. An unused lazy host allocates no gate/task/provider state.
3. `core/lazy_state.go`: native completion, including failed bodies that may have mutated output state, increments an atomic output revision. Guarded capture and typed role collection retry on concurrent changes. Container revision remains available through retained completed recipes after the operational pointer clears.
4. Persistence/encoding/visitors: snapshot links carry canonical declared paths; list encoding, capture, decode and typed role collection preserve them. Inline decoders have result ID zero plus a distinct owning-row/path carrier; copied empty maps are authoritative and mismatched carriers fail. Composite cleanup covers inline codec values and CAS losers. A public `NthValue` child borrowing an inline object retains its enclosing row and does not duplicate accessor cleanup or rebind its host.
5. Persistence/boot: all root and inline lease IDs use the scoped key. Existing root leases are re-keyed by attaching the full desired set before stale removal. Private schema 20 gains the path column/stricter key; envelope 4 and bundle 1 are unchanged. Old private-20 stores lacking the column follow `import_failure` reset, then empty desired-set reconciliation. Applied, desired and attempted role maps all use `(path, role)`.
6. `core/object.go`: declared object/interface fields received from an SDK now keep the already attached result, including string-handle and object-map inputs. Previously attachment retained the dependency but left the raw SDK value in the field; a handle string then serialized as opaque scalar JSON and escaped relocation. This uses the existing typed reference grammar and SDK conversion path. It changes internal stored representation while preserving SDK-visible values; opaque scalar fields remain scalars. The failing warm artifact case and a before/after regression establish why this additional integration fix was needed.
7. Snapshot import adds an independent pin and optional chain-content annotation mode. Ordinary image import keeps its previous mode and error behavior; a real regression checks shared chain reuse.
8. The environment-gated transfer fixture accepts selected Directory/File output IDs, carries real blob files, binds an in-process provider, and records selected/installed routes, provider reads, producer entry, sync and settlement. The unconfigured engine has no fixture schema field or event collection. The probe module now returns and reads a selected Directory artifact; the cold test is enabled.

## Verification and limits

Exact commands and outputs are in [COMMANDS.md](COMMANDS.md). Focused race checks cover:

- Own/Ready/later-route ranking, ordinary lookup control, D1 admission, sessionless subset refusal/revalidation, source expiry, canceled Commit/Finish, task isolation/continuation/NoJoin and finite decision drain.
- Admitted chain survival after donor collection and slot replacement, offer-only back-reference release after sync failure, installed-output cycle rejection before mutation, and D2 retirement of replacement slots.
- Decode after encoded Commit and before external Finish, copied empty/mismatched carriers, concurrent typed publication, scoped visitor rollback/validation and SQL conflicts.
- Real root Ready/chain/private File routes, content failure then producer fallback, partial owner-sync retry with no repeated body/read, installed-but-unsynced checkpoint/restart, target-to-receiver mount role mapping, known metadata with no layer reads, mixed Container chain restart/re-export, and fresh whole builtin invocation.
- Two inline outputs at distinct paths (including the same snapshot at both paths), independent result-ref child, concurrent publication, ordinary Nth borrowing across sessions, exact-once final accessor cleanup, clean restart and selected re-export. Nested nullable/list representation and concurrent typed collector retries are covered separately.
- Real content missing/truncation/checksum/apply failure versus writer/lease/cancellation, retained prefix, canceled waiter, native image reuse and independent pin ownership.
- The required pre-existing native retirement/cancellation/bookkeeping/group-concurrency and core direct/refined/unrefined routing regressions.

Native outcomes: **Warm PASS**, both import orders, selected artifact bytes, zero report-body count on the matching call, changed arguments, future defaults and clean restart. Assertions require provider reads, chain installation, owner sync and settlement. [Run output](logs/warm-module.log), [trace](https://dagger.cloud/dagger/traces/a2488cb947218342163d9a489b0ba81d).

**Cold FAIL**, after import and before report selection: ordinary `AsModule().Serve` requires an unavailable `mount:/schema.json`. Eager `withMountedFile`, `withMountedDirectory` and subsequent complete-parent metadata transformations have no saved producer or offer. The final A bundle preserves the selected artifact as a relocated `result_id` reference (ordinal 2), so this failure is separate from the fixed warm-field issue. The previous cold run surfaced the other pending mount, `/src`. [Final failure](logs/cold-module-excerpt.log), [bundle projection](probes/cold-closure-summary.json), [decision record](BLOCKER-2.md). No builtin-route success or zero report-body acceptance is claimed for the cold case; its assertions are not reached.

Still unproved: the full downloaded-FS plus actual privately executed execMeta engine case (including redundant FS ref release); every remaining boundary variant of the complete §10 matrix; and any end-to-end outcome past a failing native boundary. The passing two-chain mixed Container restart test is not represented as an actual mixed download/exec proof. No production service, batch 5 renewal or batch 6 sharing worker was added.

## Runtime measurements

| Measurement | Result |
| --- | --- |
| Root lease re-key, 1 role | 5.88 ms; 1 attach, 1 stale scan, 1 removal; peak 2 owner leases |
| Root lease re-key, 32 roles | 126.33 ms; 32 attaches, 1 stale scan, 32 removals; peak 64 owner leases |
| Typed collector, 1 output / depth 1 | 3.91 µs, 1,794 B, 17 allocations per scan |
| Typed collector, 128 outputs / depth 1 | 300.81 µs, 133,195 B, 1,185 allocations per scan |
| Typed collector, 512 outputs / depth 3 | 2.09 ms, 882,749 B, 5,446 allocations per scan |
| Concurrent publication, 128 outputs | 153 attempts / 25 retries / 128 accepted; 39.71 ms |
| Concurrent publication, 512 outputs | 132 attempts / 4 retries / 128 accepted; 246.83 ms |
| Native callback baseline | 0.91 ns, zero bytes/allocations per entry |
| Activated native host entry | 1.68 µs, 504 B, 13 allocations per entry |
| Ready File acquisition | 9.03 ms; 1 pin, 1 owner-sync attempt, 0 private bodies |
| Chain File acquisition | 52.66 ms; 1 pin, 1 owner-sync attempt, 0 private bodies |
| Chain with partial-sync retry | 51.53 ms; 1 pin, 2 owner-sync attempts, 0 private bodies; no repeated read |
| Failed content → private File producer | 57.88 ms; 1 pin, 1 owner-sync attempt, 1 body |
| Private File producer | 40.82 ms; 1 pin, 1 owner-sync attempt, 1 body |

[Collector/host samples](logs/collector-host-costs.log) and [real route/re-key samples](logs/route-rekey-costs.log). The restart case also passes and takes 60.04 ms in this sample; its manager counters restart on reopen, so they are not whole-attempt pin totals. File body counts observe the actual `FileBlobLazy` mutable-snapshot creation. Lifetime tests separately check release/owner balance and final collection.

These are samples on Linux/amd64 (AMD EPYC 9554P, Go 1.26.6, 16-thread benchmark setting), not stable throughput bounds. Root migration and route latency include real local snapshot/lease operations. Typed collector measurements use held guarded values without encoder, decoder, storage or identity work. The concurrent cases perform 8,192 publications and require 128 accepted complete scans; retry counts depend on scheduling. The host microbenchmark measures entry only, excluding body and storage bookkeeping. Private execution allocation totals and large e-graph scaling remain unmeasured.

No pushes, pull requests, tags, or author/reviewer contacts were made. Evidence contains focused commands, diagnostic excerpts, probes and measurements.
