# Full cold closure: producer-less pending filesystem inventory

**Count: 8 values, containing 14 pending snapshot-backed parts, have no eligible Ready local equivalent at their observed cold demand.** Seven are Containers whose 13 parts already recover through the binding Addendum 2 parent-delegation route. The remaining value is the canonical scratch Directory, ordinal **224**, with one `snapshot` part. Thus **1 value remains an unresolved acquisition boundary**. If “snapshot part” means only the literal part key `snapshot`, rather than all snapshot-backed filesystem parts, the matching count is also **1**. No additional unresolved producer-less boundary appears in this closure.

This is an inventory of the existing final run, not a new execution or a scratch implementation. The scratch decision is accepted; implementation awaits the coordinator's binding Addendum 3 message.

## Source and complete scan

- Implementation/evidence checkpoint: `6d5cbe20a42c3c82a74209eeed5901cd149dfd52`.
- **Final** cold trace: `075421afee8f5ae279ddfbd314ce854c`, selected test 153.45 s, exit 1. [Committed final route/failure excerpt](../logs/cold-addendum2-final-excerpt.log).
- Full source: `/tmp/b4-a2-validation/cold-engine-final.log`, SHA-256 `7031de4ec083276ee3ec5d7818008ca92d55564776a9ff2f151b9cae8cd448fb`. The complete `cat /source/bundles/report.json` output is split across physical lines **2058–2059**. Removing each diagnostic line prefix and joining the two chunks yields a **757,881-byte JSON document**, successfully decoded as the complete bundle. Neither the first cold run nor the earlier 13-row projection is used for this scan.
- SHA-256 of that decoded bundle re-encoded with Python `json.dumps(bundle, sort_keys=True, separators=(',', ':'))`: `24e591d89e34bb0f929bd4962ec8c54b522aea45e436286129c3d83f76dbbc95`.
- Bundle version 1; exactly **226 values**, ordinals 1–226; sole root 226. All 226 top-level envelopes are `object_self`. The filesystem inventory is **6 Directories, 1 File, 16 Containers**; the other 203 values are other object types. Every envelope's JSON was also recursively inspected, so inline/nested producer-state occurrences are not silently omitted.
- Exactly **9** JSON objects have `producerState: "none"`: ordinals **2, 10, 11, 12, 13, 17, 18, 19, 224**. Each occurs at its row's `envelope.objectJSON`, each has pending filesystem output, and each lacks both `lazyKind` and `lazyJSON`. There are no additional nested occurrences.
- The bundle has exactly **one exported chain**, at ordinal **225**, address `{"part":"snapshot"}`. None of the nine candidates has an exported chain. All records lack local snapshot links, as required for transfer.
- Ordinal 2 has a demonstrated B-local equivalent and is excluded below. The other eight are listed without dropping the seven already recoverable delegation cases.

“No local equivalent” here means **no eligible Ready equivalent for the demanded part at source selection**, not absence of every equal but pending/busy/ineligible row in the graph. This is the operational distinction needed to identify acquisition boundaries. The existing run does not retain a full dump of every B graph row. For delegated parts, the absence of an eligible Ready equivalent is inferred from the observed delegation route and the actual selector's ordering: [selectDemandPartSource](../../../../dagql/cache_part_delegation.go) returns an ordinary Ready equivalent before considering the recorded parent. It cannot choose delegation over an eligible Ready tie. After successful delegation, the imported child itself naturally becomes locally complete; this inventory refers to its preceding demand, not its post-install state.

## Every matching value

All addresses have empty `output_path`; the keys below are their complete root part keys. Receivers are **bundle ordinals**, with their recorded type and field, not result IDs from another engine. B event IDs are correlated with this run's field/part/recorded-parent chain; IDs from the first run are not reused.

| Ordinal | Type | Recorded call field | Recorded receiver | Pending part keys | Final cold-B result |
| --- | --- | --- | --- | --- | --- |
| 10 | Container | `withMountedCache` | 4, Container `_builtinContainer` | `fs` | B row 4236 delegates to 4230; installed |
| 11 | Container | `withMountedCache` | 10, Container `withMountedCache` | `fs` | B row 4237 delegates to 4236; installed |
| 12 | Container | `__withSystemEnvVariable` | 11, Container `withMountedCache` | `fs` | B row 4238 delegates to 4237; installed |
| 13 | Container | `__withSystemEnvVariable` | 12, Container `__withSystemEnvVariable` | `fs` | B row 4239 delegates to 4238; installed |
| 17 | Container | `withWorkdir` | 16, Container `withMountedDirectory` | `fs`, `mount:/schema.json`, `mount:/src` | B row 4243 delegates to 4242; all three installed |
| 18 | Container | `withEnvVariable` | 17, Container `withWorkdir` | `fs`, `mount:/schema.json`, `mount:/src` | B row 4244 delegates to 4243; all three installed |
| 19 | Container | `withoutDefaultArgs` | 18, Container `withEnvVariable` | `fs`, `mount:/schema.json`, `mount:/src` | B row 4245 delegates to 4244; all three installed |
| **224** | **Directory** | **`directory`** | **No receiver: Query root** | **`snapshot`** | **Unavailable in changed-argument control; scratch blocker** |

The seven Container rows have consumed metadata. Their pending parts are all snapshot-backed File/Directory values: fs and source mount are Directories; schema mount is a File. All 13 delegation installations above were matched against the final run's exact target ID, part key, field and parent ID. Their success is evidence that these producer-less payloads are covered by the explicit parent route, not a reason to omit them from the requested inventory. The counters report 14 delegation selections and 13 unique installations; selection repetition does not add another value or pending part.

Ordinal 224's entire payload is:

```json
{"valueKnown":true,"producerState":"none","form":"transfer_pending","dir":"/","platform":"linux/amd64"}
```

Its call has field `directory`, no receiver, and implicit input `engineDefaultPlatform = "linux/amd64"`. The final changed-argument call fails with `imported filesystem part is unavailable: {"part":"snapshot"}`. [The existing blocker and real-store probe](../BLOCKER-3.md) distinguish canonical scratch storage from a completed local Directory result; the former alone is insufficient.

## Exclusions and completeness

**Ordinal 2**, Directory `directory`, receiver **1 (Host `host`)**, also has producer state `none`, no recipe and pending `snapshot`, with no offered chain. It is excluded because the final cold run proves an eligible B-local capture: imported B row **4228** installs from local Directory row **4457**, with matching content class and installed snapshot. This is the final run's `acquisition Host input` observation and the passing assertions in [assertColdPartDelegation](../../../../core/integration/remote_cache_transfer_test.go). It is not the Query-root scratch Directory even though both call fields are named `directory`.

The other **14 filesystem values** all have producer state `completed` and nonempty saved recipe data, so they fail the producer-less predicate:

| Ordinal | Type | Field | Receiver ordinal | Recipe / selected-chain status |
| --- | --- | --- | --- | --- |
| 4 | Container | `_builtinContainer` | none (Query) | Completed Container recipe |
| 5 | Directory | `directory` | 4 | `container.directory` |
| 7 | Directory | `directory` | 4 | `container.directory` |
| 9 | File | `__schemaJSONFile` | none (Query) | `file.blob` |
| 14 | Container | `withMountedFile` | 13 | Completed Container recipe |
| 15 | Directory | `withoutFile` | 2 | `directory.without` |
| 16 | Container | `withMountedDirectory` | 14 | Completed Container recipe |
| 20 | Container | `withExec` | 19 | Completed Container recipe |
| 21 | Container | `withExec` | 20 | Completed Container recipe |
| 22 | Container | `withEntrypoint` | 21 | Completed Container recipe |
| 23 | Container | `withWorkdir` | 22 | Completed Container recipe |
| 24 | Container | `withoutMount` | 23 | Completed Container recipe |
| 25 | Container | `withoutMount` | 24 | Completed Container recipe |
| 225 | Directory | `withNewFile` | 224 | `directory.withNewFile`; sole exported snapshot chain |

Container recipes use their existing `lazyJSON` representation without a top-level `lazyKind`; absence of that field alone is therefore not treated as absence of a producer. Checking producer state and saved recipe data prevents those completed Container rows from becoming false positives.

Arithmetic: **226 = 203 other values + 14 completed-producer filesystem values + 1 producer-less Host value with local equivalent + 7 delegated producer-less Containers + 1 unresolved scratch Directory**. The 9 initial producer-less candidates contain 15 pending filesystem parts; excluding the Host snapshot leaves 8 values / 14 parts; existing delegation covers 7 values / 13 parts, leaving scratch alone.

This scan identifies every such value in the recorded closure. It does not claim that future changed-argument, restart or default controls cannot construct additional values outside that closure; those controls still require the authorized rerun after the scratch addendum binds. No engine test or cold rerun was performed, no implementation file changed, and no producer behavior was added for this evidence task.
