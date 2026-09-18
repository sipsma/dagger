# Batch 4 implementation report

**Status: complete through council round 1 decisions B4-D1–B4-D10 and notes.** All decisions are new signed commits in the requested order above the unchanged parent. All 23 final package selections pass without skips; cold, both warm import orders, mixed execution and the separately opted-in default-policy diagnostic pass through their applicable controls. Code tip: `f04a91577bd4ddc574f3bc527cdffe9fec2179a1`; the final evidence commit is separate. No reviewed commit was amended.

## Inputs and commit boundary

- Parent: `77f6279559061fd1bb6b3b18e6b08582c7b013a3`, reset as requested before implementation; its commits are unchanged.
- Commission: `10b43c933bce4afe4ee273332c79e15bcbd979c4:continuation-evidence/implementation-commissions/BATCH4-ACQUISITION-IMPL.md`.
- Base acquisition design: `2ad7bff7a4f61e39361fd222230a21bf4ffb3569`, blob `b463258caa499307b5a44032664bae37aba11623`.
- Binding Addendum 1: `04f6d695d4a944fd27e98eb006ed0cda761b3dca`, blob `289ba0f892f64eabcb01718f0f65fe1e21fb6d7f`; both consolidated council reviews and blocker decision 1 were read.
- Binding producer Addendum 2 final: `d674bea3d6f270c8fe3b2118b87bda754e26a89e`, blob `3e254e4d36a9cd80848146b223067f84d2c98ad3`.
- Binding acquisition Addendum 2 final: `7af104b49aad9dd4c9a02b42b9d2152e59a9c8fe`, blob `d63729442180a2d7faf872b687bf8d64cf19083b`. The earlier confirmed texts `0fae70578e` and `3ee2e8d1ba`, the full council consolidation `a8c33ab8e73ae64a37a07042a6e1a0adf0a4211d`, and both editorial diffs were read. The editorial changes add no rule.
- Binding producer Addendum 3 final: `435db0d9ab177aedb2f33f5efb4a93f8dc3bc72c`, blob `a07753c805cde2889e3e7aa04570ad5f20883f13`, sections A3.1–A3.5. Both Addendum 3 council consolidations were read in full, including the explicit raw empty-object validation requirement.
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

All implementation commits are buildable and signed off. Step 5's cold acceptance is now supported by the complete passing controls below.

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
| `f134133fc8` | Record the two completed eager mount recipes; preserve exact inputs and re-create fresh recipes |
| `c02411efad` | Closed exact-parent delegation, private proof/selector/path, gated observations and local pending restore |
| `21d0da8c86` | Actual resolver rejection/cleanup checks, including borrowed-input preservation |
| `3d90f1f724` | Release shadowed parent mount clones on eager construction failure |
| `ff0879dfcb` | Source-ordering/waiter tests, exact proof checks, shifted roles, A→B→C, native restart and depth costs |
| `3337963558` | Cold exact-row/full-address SDK and Host counters, separated from producer entries |
| `4ccf106dd4` | Report all inherited mount-producer routes with exact row/address and recorded parent |

New signed commits for the binding Addendum 3 dispatch:

| Commit | Change |
| --- | --- |
| `f9158c2ba2735be974770246a2449daee0b106c2` | Eager scratch recorder, empty-object codec/visitor, private body and generic harness cases |
| `b404de7252` | Scratch real-store acquisition, independent ownership, native/pending restarts and cost benchmark |
| `78eab47380` | Exact scratch counters through both module controls and gated full-closure import mapping |

The final separate evidence commit is listed in the delivery reply. [Complete commit order](COMMITS.md) includes the original five buildable implementation steps and the unamended blocker/addendum history. No reviewed commit or parent commit was amended.

## Council round 1 decisions implemented

The full consolidation, DECISION-1 and coordinator review at `418321a5e1d496ceeaeee132fe5bf98722ac0b1b`, plus the six cited seat reviews (`48e54f1746`, `b493a1b2be`, `04cbbb0168`, `241f473fbc`, `17e7a231a6`, `30520825e3`), were read in full. Each decision is a new signed commit in the requested order; the final notes are separate. No reviewed commit was amended.

| Decision | Commit | Result |
| --- | --- | --- |
| B4-D1 | `733cec2bb0` | Retain attached results only for ID-shaped object/interface inputs; preserve inline maps and dependency edges; retain relocation regression and add raw ParentFields regression |
| B4-D10 | `c62429de3f` | Prepare sessionless lookup outside E, revalidate frame/admission under E; real imported pending Directory with numeric receiver survives checkpoint/reopen and balances donor ownership |
| B4-D7 | `855004e49f` | Erroring scans return no owning winner and preserve cleanup errors; decision refusal keeps release errors; Ready and chain winner cases balance holds |
| B4-D5 | `3b3a076e1f` | ImportChain's returned ref has retryable cleanup transferred at Commit to row and continuation, without a graph hold; failure retries before sync or at receiver collection without another import |
| B4-D6 | `ded91500c9` | Final producer and pending-parent failures retain accumulated chain causes; both failure branches and successful fallback are covered |
| B4-D2 | `66a1822fa6` | Exhaustion includes admitted offer revision; a replacement with identical immutable content and a new revision remains selectable |
| B4-D3 | `6853575364` | Native completion uses D2 after sync/operation cleanup and retires an offer arriving during its native body |
| B4-D8 | `0829066c7e` | Range remains first choice; 200 responses stream consecutive reads, reissue for seeks, and close on cancellation; multi-buffer request counts are checked |
| B4-D4 | `9f71286d8c` | Remove the dead Container pending carrier and all its reads, including recorder/raw-view fallbacks; Directory/File carriers remain |
| B4-D9 | `edd08565fc` | Derive structural input refs once, remove unused private revision copies, compare validated part addresses directly |
| Notes | `f04a91577b` | Validate imported dependency mappings against reported non-root rows; assert that only the private producer's FS handle receives the release observer |

[Complete history and packaging notes](COMMITS.md) records the object-fix packaging candidates, fixture additions for batch 7, two exported helper surfaces, shared resource-pin retry/chain-mode paths, and the pure per-row `restoredDelegation` marking under P. Depth-d latency is stated in prose with the current measurements below.

## Implemented behavior

The acquisition collector inspects copied records without decoding or opening storage, checks the owner's completed output first, then ranks all ordinarily eligible equivalents Ready before chain before saved producer, with stable route/ID ordering. Ordinary lookup/e-graph selection code is unchanged. D1 checks offer-owner dependencies' own resource requirements with the foreground session. The private sessionless Ready constructor accepts only imported receivers and current own-requirement subsets, with lookup preparation outside E and frame/admission revalidation under E and at Commit.

Prepare owns an independent snapshot pin, exact output/reference holds, complete next record and typed accessor preparation. Commit validates generation, gate, representation revision, source authority, roles and dependency cycles before the E→G→core-guard→P store. Ready sources stay held through Commit; admitted chains retain copied offer authority independently of source row or slot. Publication hands off offer/donor ownership before bookkeeping. Finish consumes its bounded receipt hold, supports the owner-sync barrier, and continues cleanup → owner sync → pin release → D2 without rerunning a body/download. Sync-pending continuation state has no self-hold or retained offer owner. The ImportChain-returned ref uses the same retryable pre-sync cleanup handoff as private producer refs: Commit installs its cleanup on both row and continuation, so bookkeeping or final receiver collection retries a failed release without another download.

Actual `ImportChain` receives fixed-address or in-process providers. Only supplied chain/content failures become `ChainContentError`; writer/lease/local storage and cancellation retain their ordinary classification. Demand exhaustion uses source identity, full address, value, ordered content and admitted offer revision. Address text alone does not reset it; a newly admitted replacement revision remains eligible. Final producer and pending-parent failures join accumulated acquisition causes without changing pure reselect or cancellation handling. Private producer attempts decode fresh recipes and accessors; they validate the demanded output and complete write set, preserve previously final outputs, and release redundant private outputs. Container raw metadata/parts/roles/producer data live in one immutable versioned view independently of typed producer latches.

Addendum 1 is implemented in dependency order: path/key/SQL helpers, link preservation, visitor/capture projections, decode carrier and core loader guards, guarded typed collection, scoped ownership and boot restoration, then representation/race/real-store cases. Inline items retain the enclosing row and full path; a `result_ref` changes owner. Snapshot link copies use the one internal `cloneSnapshotRefLinks` helper (core calls its facade). Duplicate `(result_id, output_path, role)` inserts are checkpoint errors. The desired-set stale-lease scan runs at every cache boot, including no persisted rows and after `import_failure` reset; a failed scan fails initialization.

`InstalledOutputs` is declared. `PartDemandState` leaves room for batch 5's second keyed set. Prepare/Commit/Finish arity is unchanged; no batch 6 ordered-base or predecessor provenance fields were implemented.

Addendum 2 records the original typed parent/source, resolved target, effective owner and readonly option at the two eager mount resolvers. The narrow recorder checks construction ownership and completed accessors without evaluating or loading anything, then stores only `completedRecipe`. The parent's existing completed-recipe attachment fallback is retained and tested. Failure cleanup covers the fresh child and mount clones shadowed by the body, uses an uncanceled context and preserves cleanup errors.

Delegation uses the exact receiver dependency for the ten closed core fields, with no saved recipe and consumed metadata. It preserves child metadata and translates mount target/kind to the child's own positional role. The private selector keeps own final outputs and ordinary Ready ties first, admits a Ready parent before a chain, and demands a pending parent only after the existing routes. Public equivalent-source selection keeps its prior meaning. A copied context path detects repeated row/full-address pairs before task joins. The child holds no write permit while awaiting the parent, and an already waiting caller waits for parent completion even when the child independently installs first.

The delegated source proof binds both exact registrations, frozen frames, receiver edge/mapping, representation and descriptor versions, requesting-session resources and full addresses. Normal Prepare/Commit/Finish supplies independent pins, accessor refs, child roles, cycle checks and owner sync. The temporary parent hold drops at Commit/refusal before Finish or sync-failure retention. Never-imported pending metadata children decode into the same managed raw representation only when the closed mapping is valid; unsupported native missing-recipe states still fail.

The Addendum 2 fixture `Source` field carries parent ID/full address only for `selected-delegation` and `installed-delegation`. The cold test reports these in its acquisition counters and reports mount producer entries separately. No production event collection is enabled by default, and no producer is inferred from a field name.

Addendum 3 records a no-input `DirectoryScratchLazy` only at the successful eager `Query.directory` resolver. Its recipe is `{}`; decode and the reference visitor share explicit raw validation rejecting absent, null, non-object and nonempty payloads. Capture retains the recipe beside native ownership, foreign normalization preserves it on the pending part, and native/pending reopen reuse the existing formats and routing. The private body validates fresh accessors, calls the local canonical scratch helper, preserves the receiver's persisted platform and defers temporary-ref cleanup inside its lazy-state closure. It transfers no bytes and adds no recipe input edges. Neither `RouteParts` nor the private invoker changed for scratch.

The real-store cases cover independent B ownership with no canonical snapshot, canonical storage only, an eligible warm row, failures before Commit, owner-sync retry after Commit, and concurrent demands. They preserve A's arm64 platform under B's amd64 default. Native save/reopen runs no producer; still-pending save/reopen runs exactly one, and the next reopen restores locally without another. Ownership assertions inspect individual ref releases so the permanent canonical lease cannot hide borrowed ownership. A's closure and manager can close, and a warm donor can collect, while B's installed accessor remains readable.

## Ordinary execution changes

These are the changed paths and their intended effects:

1. `dagql/cache.go` and `cache_part_task.go`: the shared lazy kernel gains supplied bodies, generation and continuation state; native callbacks retain ordinary retirement, cancellation and bookkeeping semantics. Native final transitions now run D2 offer retirement after owner sync and operation cleanup, retaining bookkeeping on error. Synthetic completion does not set native whole-result completion.
2. `core/file.go`, `directory.go`, `container.go`, `container_parts.go` and `filesystem_output.go`: attached callbacks enter a stable host/gate before body latches. Detached execution remains direct. File/Directory complete tuples are guarded; raw Container readers see one version. Opening an acquired output follows demand before reading its descriptor. An unused lazy host allocates no gate/task/provider state.
3. `core/lazy_state.go`: native completion, including failed bodies that may have mutated output state, increments an atomic output revision. Guarded capture and typed role collection retry on concurrent changes. Container revision remains available through retained completed recipes after the operational pointer clears.
4. Persistence/encoding/visitors: snapshot links carry canonical declared paths; list encoding, capture, decode and typed role collection preserve them. Inline decoders have result ID zero plus a distinct owning-row/path carrier; copied empty maps are authoritative and mismatched carriers fail. Composite cleanup covers inline codec values and CAS losers. A public `NthValue` child borrowing an inline object retains its enclosing row and does not duplicate accessor cleanup or rebind its host.
5. Persistence/boot: all root and inline lease IDs use the scoped key. Existing root leases are re-keyed by attaching the full desired set before stale removal. Private schema 20 gains the path column/stricter key; envelope 4 and bundle 1 are unchanged. Old private-20 stores lacking the column follow `import_failure` reset, then empty desired-set reconciliation. Applied, desired and attempted role maps all use `(path, role)`.
6. `core/object.go`: the council's B4-D1 decision limits retention to ID-shaped declared object/interface values (`dagql.AnyResult`, `dagql.IDable`, and validated handle strings). These keep the attached result so persistence can relocate it. Inline object maps retain their raw SDK representation and still attach dependency edges. The handle relocation regression and the inline-map raw `ParentFields` regression cover both boundaries. Opaque scalar fields remain scalars.
7. Snapshot import adds an independent pin and optional chain-content annotation mode. Ordinary image import keeps its previous mode and error behavior; a real regression checks shared chain reuse.
8. The environment-gated transfer fixture accepts selected Directory/File snapshot IDs and Container FS IDs, carries real blob files, binds an in-process provider, and records selected/installed routes, provider reads, producer entry, sync and settlement. Its report includes copied applied snapshot links. With observation enabled, a private Container's actual FS ref is wrapped after execution to report successful or failed `Release` after the underlying call returns; the wrapper neither replaces the snapshot manager nor simulates execution. The unconfigured engine has no fixture schema field or event collection, and private refs are not wrapped. The probe module returns and reads a selected Directory artifact; the cold test remains enabled; this dispatch adds route assertions without changing its setup, export selection, cold ordering or existing controls.

9. `core/schema/container.go` and `core/completed_producer.go`: the two eager mount calls now retain completed recipes. They remain eager with the same values/options and borrowed inputs. Their new invariant failure path releases construction-owned refs, including shadowed clones; owner/body failure cleanup also covers the successfully cloned child. Tracking the original mount list adds a shallow slice copy to those construction paths.
10. `core/part_routes.go`, `dagql/cache_part_delegation.go` and the demand/Commit paths: eligible missing parts can acquire from the exact recorded parent, with the specified ordering and waiter latency. This affects managed missing-part demands, not ordinary request/egraph lookup.
11. `core/container.go` and `dagql/cache_persistence_import.go`: a never-imported locally persisted pending metadata child with a valid mapping is now admitted for foreground acquisition after decode. Boot only marks the pure mapping and does no producer/provider work; ordinary completed local restore remains on its existing path.

12. `core/schema/directory.go`, `core/directory_scratch.go`, `core/directory.go` and the Directory visitor map: eager scratch creation retains an empty completed recipe and fresh latch. The existing eager snapshot call, platform identity, path and returned value are unchanged. Recorder failure now fails the call and releases construction ownership; the wrapping-error path also preserves cleanup errors. A transferred scratch row with no available source can now recreate its local canonical output through the saved producer. Own/Ready ranking remains ahead of that body. The gated import fixture additionally returns mappings for dependency rows, rooted in the current import allocation and validated against independently reported non-root row metadata, so the acceptance counter locates the actual imported Directory without a fixed ordinal; existing root mappings remain first.

## Isolated object-field correction

The failing real case was the warmed module report's declared Directory artifact. After transfer, ordinary `artifact.file(path:"payload.txt").contents` followed an A-engine handle left in scalar JSON and selected an unrelated B Directory. Both warm orders failed before this fix; [the failure excerpt](logs/warm-before-fix-excerpt.log) and [the before regression](logs/declared-handle-before.log) establish the defect. The focused regression expected `result_id` but received `scalar_json`. After the fix, the same field enters `VisitEncodedReferences`, changes to the relocated ID, and preserves ordinary SDK conversion and dependency lifetime. Both warmed engine orders pass, including artifact contents and restart.

The fix was initially bundled in `521b90d51d`. To honor the new request without rewriting reviewed history, `8e0e2198d8` removes exactly that change and `5b73480d43` reapplies it alone. The inverse commit compiles; the isolated fix passes the same focused object/relocation race selection ([output](logs/object-isolated-race.log)).

The narrower dependency-only approach already existed and is exactly what failed: ownership alone cannot relocate a field represented as an opaque scalar. Parsing arbitrary strings as handles in generic persistence would reinterpret legitimate scalar strings; changing only the gated module fixture would conceal the real SDK boundary defect. Retaining only ID-shaped values at the declared Object/Interface conversion point uses type information and the established reference grammar. The original broader retention also converted inline maps to handles; B4-D1 restores their prior SDK representation and adds a regression. No generic scalar conversion changes. The isolated object-field commits are candidates for separate packaging ahead of the acquisition change. The new inline-map test fails against the preceding broad retention implementation, then passes against the narrowing; only the compared production file was temporarily replaced and restored byte-for-byte. [Before](logs/round2-d1-inline-before.log), [after and retained handle cases](logs/round2-object.log).

## Final council round 2 verification

[Exact command ledger](COMMANDS-ROUND2.md) and [package manifest](validation-round2.json) record all 23 sequential selections (385.956 s total invocation time), including the commission's native regressions, every previously passing selection, actual snapshot stores, all new decision cases, and current runtime samples. None skipped. Individual pre-commit checks and the expected inline-map before-failure are retained separately. The historical exact-parent gap probe was rerun separately and is not counted as recovery acceptance.

| Engine selection | Selected test | Full invocation | Result |
| --- | --- | --- | --- |
| Cold recovery | 179.13 s | 451.179 s | Passed, no skips |
| Warm, both import orders | 249.14 s | 294.029 s | Passed, no skips |
| Downloaded FS / private execMeta | 71.14 s | 136.196 s | Passed, no skips |
| Opted-in default-policy pruning | 132.22 s | 212.751 s | Passed, no skips |

**Cold recovery passes every control** (trace `d0b36d6dddc1270841e7847f6230c58f`). Import precedes B's module runtime, with only the selected artifact chain exported. The matching report uses zero report-body entries; changed arguments produce exactly one. There are 13 installed exact-address delegations, both eager mount writers run once, the inherited SDK FS hops use their recorded parents, and Host row 4228 acquires the matching local row 4457. Delegation observations remain separate from producer observations. [Counters](probes/round2-cold-engine-counters.json), [focused log](logs/round2-cold-engine-counters.log), [complete 226-value closure](probes/round2-cold-closure.json).

After the changed call, the re-read report identifies the no-receiver Directory call `directory`: ordinal 223, B row 4449, full address `snapshot`, group `whole`, platform `linux/amd64`. It has exactly one scratch producer entry and zero provider reads; repeated demand leaves that count at one. The ordinal is observed, not fixed in the test. The later note File and bound-tool controls pass. Clean non-pruning restart retains report row 4452 with no reset and zero removed roots; note Directory access succeeds and report-body count stays one. The higher native-recorded Module row 4942 with a lower imported equivalent and the foreign-context control both pass.

Both warm orders pass, each with zero scratch producer entries/provider reads, including repeated demand. Both clean restarts retain report-body count one; native-recorded and foreign-context controls pass. [Warm observations](logs/round2-warm-engine-counters.log), [counter data](probes/round2-warm-engine-counters.json).

The mixed engine proof preserves the downloaded FS, runs the private execMeta producer once, and releases its distinct redundant FS before sync. Repeated reads run no second producer. Fresh private execMeta demand measured **183.632064ms**; original FS `p6bxegj7oe64pcuoxqakd20x0` and released private FS `nqgayzraq93v8colnes48pb1b` differ. The gated concrete ref decorator is only on the private producer's FS handle; the focused assertion preserves ordinary/receiver FS, execMeta and mount ref types. [Mixed observations](logs/round2-mixed-engine-counters.log), [scope assertion](logs/round2-notes-private-ref.log).

The default-policy diagnostic is separate from cold non-pruning restart. With the existing temporary runner opt-in, it requested 2,889,933,312 bytes of pressure under the 8,589,934,592-byte cap, removed 17 persisted roots, explicitly included report row 4697 in pruning, and confirmed its absence after reopen with no reset. Matching/changed report, zero-entry warm scratch and later File/tool controls passed before pressure. No unreached clean-retention assertion or skip is counted as a pruning pass. The runner was restored byte-for-byte. [Observations](logs/round2-default-policy-opted-in-counters.log), [counter data](probes/round2-default-policy-opted-in-counters.json), [runner overlay](probes/default-policy-runner.patch).

[Engine manifest](validation-round2-engines.json) records trace IDs and timings. The following section preserves the earlier Addendum 3 evidence at `78eab47380`; it is historical, not substituted for these current-source reruns.

## Earlier Addendum 3 verification and limits

Exact commands and outputs are in [COMMANDS.md](COMMANDS.md). Focused race checks cover:

- Own/Ready/later-route ranking, ordinary lookup control, D1 admission, sessionless subset refusal/revalidation, source expiry, canceled Commit/Finish, task isolation/continuation/NoJoin and finite decision drain.
- Admitted chain survival after donor collection and slot replacement, offer-only back-reference release after sync failure, installed-output cycle rejection before mutation, and D2 retirement of replacement slots.
- Decode after encoded Commit and before external Finish, copied empty/mismatched carriers, concurrent typed publication, scoped visitor rollback/validation and SQL conflicts.
- Real root Ready/chain/private File routes, content failure then producer fallback, partial owner-sync retry with no repeated body/read, installed-but-unsynced checkpoint/restart, target-to-receiver mount role mapping, known metadata with no layer reads, mixed Container chain restart/re-export, and fresh whole builtin invocation.
- Two inline outputs at distinct paths (including the same snapshot at both paths), independent result-ref child, concurrent publication, ordinary Nth borrowing across sessions, exact-once final accessor cleanup, clean restart and selected re-export. Nested nullable/list representation and concurrent typed collector retries are covered separately.
- Real content missing/truncation/checksum/apply failure versus writer/lease/cancellation, retained prefix, canceled waiter, native image reuse and independent pin ownership.
- The required pre-existing native retirement/cancellation/bookkeeping/group-concurrency and core direct/refined/unrefined routing regressions.

The final dispatch's exact sequential commands and outputs are in [COMMANDS-ADDENDUM3.md](COMMANDS-ADDENDUM3.md); earlier ledgers preserve history. All 23 package selections passed without skips, including every previously passing implementation selection, original native kernel/routing regressions, scoped inline/boot cases, real snapshot fault controls, recorder/delegation checks and the new scratch cases. The gated fixture mapping follow-up also passes. [Package manifest](validation-addendum3.json).

The warm engine selection passes both import orders and their subsequent clean restarts (270.93 s selected test; 524.731 s invocation). Each exact imported scratch row has zero producer entries and zero provider reads, including repeated demand. Report-body count remains one at each restart; native-recorded and foreign-context controls pass. [Warm counters](probes/warm-addendum3-counters.json), [trace excerpt](logs/addendum3-warm-counters.log).

The real mixed downloaded-fs/private-execMeta engine selection passes (70.81 s selected test; 146.698 s invocation). Its fresh demand sample is 174.84 ms; the installed original FS and released redundant private FS have distinct identities. It preserves the downloaded FS, executes execMeta once, releases the redundant FS before sync and does not repeat execution on later reads. [Mixed counters](logs/addendum3-mixed-counters.log).

**Cold recovery now passes.** The final scratch implementation run (trace `3df9e6ae55a1d589249c550bb46a3bb4`) completes the selected test in 212.04 s (316.715 s including build/startup/cleanup). B imports before constructing its module runtime, and only the artifact chain is exported. The matching report reads its selected bytes with zero report-body entries. Both eager mount writers run once, 13 exact row/address delegations install, the SDK cache/system-env hops use the recorded parents, and the imported Host input acquires B's matching capture. Acquisition and producer observations remain separate.

The changed-argument report succeeds and reaches report-body count one. The re-read report identifies the imported no-receiver Directory call `directory`: ordinal 224, B row 4450, recorded platform `linux/amd64`, full address `snapshot`, group `whole`. Exactly one scratch producer entry and zero provider reads are observed; an explicit repeated empty-directory demand leaves the entry count at one. These ordinals/IDs are this run's observations, not constants in the test. [Counters](probes/cold-addendum3-counters.json), [trace excerpt](logs/addendum3-cold-counters.log), [all 226 closure rows](probes/cold-addendum3-closure.json).

The later note File and bound-tool controls pass. Clean non-pruning restart retains saved report row 4452 with no reset and zero removed persisted roots. Note Directory access succeeds after restart and the report-body count remains one. The higher native recorded Module with a lower imported equivalent passes its bare-client control; the foreign-context subtest also passes. Historical [blocker 3](BLOCKER-3.md) and its gap probe remain evidence of the earlier behavior, superseded by the binding scratch producer and these reached controls. The first Addendum 3 counter run succeeded at the changed report but failed the newly added row lookup because the fixture returned only roots; [that diagnostic](logs/addendum3-counter-mapping-before.log) led to the gated full-closure mapping regression, not a change to cold order or exported bytes.

The default-policy variant was run separately. Its initial env-file-only invocation skipped before the opt-in gate because the runner only mounts that file; it is not counted as a pass. The opted-in invocation used a temporary one-line runner environment setting, restored byte-for-byte afterward. The existing diagnostic then passed without skips: 3.59 GB requested temporary pressure under the 8 GiB cap, 17 removed persisted roots, saved report row 4697 explicitly in the prune decisions and absent after reopen, and no cache reset. Matching/changed report, warm scratch and pre-pressure contextual controls passed first. [Default-policy counters](probes/default-policy-addendum3-counters.json), [trace excerpt](logs/addendum3-default-policy-counters.log), [runner setting and exact command](COMMANDS-ADDENDUM3.md). The cold non-pruning and this default-pruning result are distinct controls; no skip or unreached assertion is presented as a pass.

The historical parent probe passed again after these selections on the unchanged integrated head. At that dispatch the code matched the council's round-1 pin; its separate evidence commit added only evidence and wording corrections. [Earlier engine manifest](validation-addendum3-engines.json).

The original warm proof and mixed actual exec proof remain documented in the historical [warm output](logs/warm-module.log), [mixed output](logs/mixed-exec-engine.log) and [mixed measurement](logs/mixed-exec-measurement.log). The mixed test uses two real dev engines, downloads FS independently, privately executes execMeta once, preserves the installed FS, releases the distinct redundant private FS before sync, and does not rerun on repeat reads. Current-dispatch reruns are listed in the new command ledger.

The remaining independent boundary follow-up passes focused race selections: actual late Ready and chain arrival during private preparation with zero private runs; a second stale preparation releases both real pins and returns reselect; ordinary and NoJoin conflicts during an inline decision; sibling admission before that decision's sync finishes; a late offer cannot interrupt Running; cancellation during pin preparation prevents publication and balances ownership; a broken local Ready descriptor returns its storage error. A decode paused on an old representation loses to a real new installation, releases the temporary value once, and sees the winning complete roles. File and Directory tuple publication races cover path, platform, service and snapshot coherence plus guarded role readers. A failed retained-pin release retries without downloading again or repeating successful owner sync. [DagQL output](logs/boundary-dagql.log), [core output](logs/boundary-core.log).

The whole-producer restart case uses a real saved `_builtinContainer` recipe and a controlled valid mixed representation with FS transferred and execMeta pending. It checkpoints/reopens, performs no provider or producer work at boot, then invokes a fresh whole builtin, preserving the first installed FS and the raw recipe. The builtin resolves execMeta to **absent** and releases its redundant FS. This is a representation/whole-invoker proof, not a claim that builtins produce exec metadata snapshots; the real mixed exec case above proves the non-absent variant. Native pending Containers without a recipe still error outside the imported adapter or the newly authorized exact-parent mapping. Pending image metadata also runs only its metadata producer and leaves FS pending with no snapshot open or provider read. [Whole/native output](logs/whole-restart.log), [pending metadata output](logs/pending-image-metadata.log).

A Ready donor with a direct donor→receiver dependency collects before external Finish opens the owner's sync barrier. The published receiver does not gain a donor edge, and it collects after its final session and persisted owners release. [Backreference output](logs/ready-backreference.log).

New focused tests additionally pass all ten pure mappings and unsupported/malformed frame controls; Ready-parent versus child-chain ranking; ordinary Ready ties; chain-before-pending-parent fallback; a paused parent with independent child installation and no early waiter return; parent-hold release with the child pin retained after partial sync failure; copied path cycle/sibling controls; surviving File mount role `mount_file:1` → `mount_file:0`; wrong-kind refusal; A→B→C relocation without local links; and never-imported local pending restart at depths 1, 8 and 32. Exact tests and artifacts are linked in the new ledger. No batch 5 renewal, batch 6 sharing worker or intrinsic-scratch shortcut was added. The scratch body is reached only through its typed recorded recipe and the existing source ordering.

## Runtime measurements

These are current-source samples, including the requested root lease re-keying, wide/nested collector traversal with publication retries, and depth-d bookkeeping.

| Measurement | Round 2 sample |
| --- | --- |
| Root lease re-key, 1 role | 4.57 ms; 1 attach, 1 stale scan, 1 removal; peak 2 owner leases |
| Root lease re-key, 32 roles | 135.16 ms; 32 attaches, 1 stale scan, 32 removals; peak 64 owner leases |
| Typed collector, 1 output / depth 1 | 2.27 µs, 1,792 B, 17 allocations per scan |
| Typed collector, 128 outputs / depth 1 | 194.77 µs, 133,103 B, 1,185 allocations per scan |
| Typed collector, 512 outputs / depth 3 | 1.29 ms, 882,072 B, 5,444 allocations per scan |
| Concurrent publication, 128 outputs | 151 attempts / 23 retries / 128 accepted; 30.93 ms |
| Concurrent publication, 512 outputs | 136 attempts / 8 retries / 128 accepted; 140.96 ms |
| Empty native callback baseline | 0.87 ns, zero bytes/allocations per entry |
| Activated native host entry | 958.7 ns, 504 B, 13 allocations per entry |
| Ready File acquisition | 11.12 ms; 1 pin, 1 sync attempt, 0 private bodies |
| Chain File acquisition | 57.03 ms; 1 pin, 1 sync attempt, 0 private bodies |
| Partial-sync retry | 63.32 ms; 1 pin, 2 sync attempts, no repeated download/body |
| Failed content → private File producer | 120.91 ms; 1 pin, 1 sync, 1 private body |
| Private File producer | 46.61 ms; 1 pin, 1 sync, 1 private body |
| Native restored delegation, depth 1 | 8.53 ms; 1 independent accessor ref, pin and child owner-sync step |
| Native restored delegation, depth 8 | 78.88 ms; 8 independent accessor refs, pins and child owner-sync steps |
| Native restored delegation, depth 32 | 559.35 ms; 32 independent accessor refs, pins and child owner-sync steps |
| Scratch eager fixture without recipe | 321.5 ns, 432 B, 4 allocations |
| Same fixture with completed recipe | 402.3 ns, 472 B, 6 allocations; delta 80.8 ns, 40 B, 2 allocations |
| Scratch encoded overhead | 2-byte recipe; 35 added bytes in the native Directory payload |
| Cold scratch / canonical-only / warm | 37.59 / 14.88 / 10.53 ms; private Scratch calls 1 / 1 / 0; zero provider reads |
| Still-pending scratch restart acquisition | 34.28 ms; one private Scratch call, no provider reads |
| Scratch owner-sync retry | 32.10 ms; one private Scratch call across failure and retry |
| Fixed HTTP, 320,000 bytes / 16 KiB buffers | 20 reads: 20 requests for 206 Range responses, 1 request for a Range-ignoring 200 endpoint |

The native restored delegation samples took **8.53, 78.88 and 559.35 ms at depths 1, 8 and 32** respectively. Each hop installs an independently owned view with its own accessor, pin and durable owner-sync step. Temporary pins release after sync; accessors and owner leases remain with their rows. These timings include capture/probe and graph work, retain the receiver edges, and imply no constant-time per-hop guarantee. No producer body or download ran in these Ready-ancestor samples. [Depth measurements](logs/round2-delegation-costs.log).

The fixed HTTP provider favors Range: each read buffer costs a separate request when the endpoint returns 206. A 200 response retains one response body, offset, mutex and cancellation callback, allowing sequential reads through one request without a new blob buffer. Reads on a reader are serialized. A non-sequential offset closes that response and requests again; when Range is ignored again it discards the prefix to reach the requested offset. This trades retained response state for endpoint compatibility and fewer sequential round trips. Both restart and rewind controls pass, as does cancellation closing the retained stream. These are request counts against a local HTTP endpoint, not network throughput measurements. [HTTP controls](logs/round2-d8.log).

[Collector/host samples](logs/round2-collector-costs.log), [real routes and re-keying](logs/round2-route-costs.log), [scratch real-store samples](logs/round2-scratch-costs.log), [recording benchmark](logs/round2-scratch-recording-costs.log). Linux/amd64, AMD EPYC 9554P, Go 1.26.6. The collector race performs 8,192 publications and requires 128 accepted scans at each width; retries vary with scheduling. The empty callback/host benchmark excludes bodies, settlement and storage, so its ratio is not a whole-operation regression. File chain timing uses an in-process provider, not HTTP. The 168.16 ms chain-restart sample resets counters on reopen and is not a whole-attempt pin total. The eager recording benchmark excludes existing eager snapshot I/O. Private execution allocation totals and larger-egraph scaling remain unmeasured.

The current mixed real-engine demand sample is 183.632064ms; it includes actual private execution and is distinct from the File microbenchmarks.

No pushes, pull requests, tags, or author/reviewer contacts were made. Evidence contains focused commands, diagnostic excerpts, complete closure metadata, counters and measurements.
