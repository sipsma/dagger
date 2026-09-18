# Blocker 3: imported canonical scratch Directory has no acquisition route

The two binding Addendum 2 mechanisms are implemented. The cold engine run now passes ordinary B `AsModule().Serve`, the matching report/artifact read, all new route assertions, and the echo control. It fails next at the existing changed-argument report control, when its ordinary `dag.Directory().WithNewFile(...)` needs the imported scratch Directory's `snapshot` part. No new producer or intrinsic-scratch rule has been improvised.

Binding text: producer Addendum 2 at `d674bea3d6f270c8fe3b2118b87bda754e26a89e`, acquisition Addendum 2 at `7af104b49aad9dd4c9a02b42b9d2152e59a9c8fe`, and council consolidation at `a8c33ab8e73ae64a37a07042a6e1a0adf0a4211d`. The editorial follow-ups changed no rule. Their scope records the two eager Container mount producers and the closed ten-field Container delegation table; it does not add a scratch Directory producer or an intrinsic Directory state.

## Engine evidence

Command: `dagger api call engine-dev test --pkg ./core/integration --run='TestRemoteCacheTransferSuite/TestSchemaRecoveryCold$' --timeout=5m --test-verbose`. Exit 1; selected test duration 154.12 seconds. Trace ID `153bdebcb2fa21f394410d472b883849`. [Focused output](logs/cold-addendum2-excerpt.log).

The ordinary cold order remains unchanged: separate engines/clients/checkouts; only the report artifact's chain is exported; B imports before AsModule, SDK warmup or Serve. The actual [closure projection](probes/cold-addendum2-closure-summary.json) contains 226 values, report root ordinal 226 and exactly one selected chain, artifact ordinal 225. Scratch is ordinal **224**, with this payload and no selected output:

```json
{"valueKnown":true,"producerState":"none","form":"transfer_pending","dir":"/","platform":"linux/amd64"}
```

Its call is core `Query.directory`, with no receiver and the `engineDefaultPlatform=linux/amd64` implicit input. It has neither `lazyKind` nor `lazyJSON`; local snapshot links were stripped normally.

Reached observations before the failure:

- Both saved mount writers run once: B row 4241 `mount:/schema.json` and row 4242 `mount:/src`.
- Thirteen delegated installations have matching selection events and exact recorded-parent/source addresses. Rows 4243–4245 each acquire fs and both mounts without a metadata-transform producer entry.
- Inherited fs passes both cache children (4236, 4237), both system-environment children (4238, 4239), and the builtin route. The builtin route count is 1.
- Imported Host input row 4228 acquires B's matching local Directory row 4457. Their content class and installed snapshot agree.
- The matching report has zero report-body entries and returns the selected artifact bytes. The selected artifact causes one provider read and one chain installation. The echo control also passes.

The next `report(seed:"different")` enters the ordinary function and fails while constructing/reading its artifact:

```text
imported filesystem part is unavailable: {"part":"snapshot"}
```

This stops the existing changed-argument assertion before its post-call body-count check and stops the later cold restart/default controls. Those controls are not claimed to pass.

### Final rerun

The same command fails at the same changed-argument call again: exit 1, selected test 153.45 s, full invocation 195.036 s, trace ID `075421afee8f5ae279ddfbd314ce854c`. [Final focused output](logs/cold-addendum2-final-excerpt.log). The cold `foreign_context` subtest passes independently. Warm schema recovery and the mixed actual exec selection both pass in the final sequential rerun; [manifest](validation-addendum2-engines.json).

The final run reports mount File row **4240**, parent 4239, entering once at each of `fs` and `mount:/schema.json`; mount Directory row **4242**, parent 4240, enters once at each of `fs`, `mount:/schema.json` and `mount:/src`. These are producer entries, reported separately from the 13 delegated installations. The earlier row 4241 and closure projection above belong to the first run. Both runs use exact row/full-address assertions rather than assuming stable row numbers.

Final counters are `installed-chain:1`, `installed-delegation:13`, `installed-producer:24`, `installed-ready:1`, `owner-sync:138`, `producer-enter:24`, `producer-ref-released:14`, `provider-read:1`, `selected-chain:1`, `selected-delegation:14`, `selected-ready:1`, `settled:45`. The 14 selection observations include a repeated selection; each of the 13 target row/full-address installations is asserted exactly once. The matching report still has zero body entries before the changed-argument call.

## Minimal real-store probe

[Probe source](probes/scratch_acquisition_gap_test.go.txt) invokes the actual schema `directory` resolver on A, captures and exports it, then demands its exact imported row on B. Both B variants create/open the real canonical scratch snapshot in their own snapshot manager. The cold variant has no local Directory result; the warm variant additionally performs ordinary B `Query.directory` before import. No snapshot manager or import mechanics are faked.

[Probe output](logs/scratch-gap-probe.log) confirms:

| Case | Result |
| --- | --- |
| Native actual resolver | Snapshot form, local role `dagger-scratch-rootfs-v1`, no saved producer |
| Export | Pending snapshot, producer state `none`, no chain |
| Cold B, canonical snapshot already in B storage | Exact imported demand returns `ErrUnavailablePart` |
| Warm B, ordinary completed Directory row also available | Exact imported demand succeeds through local equivalence |

The probe passes by asserting the gap and its warm control. It is not an acceptance pass for cold acquisition. Its temporary test file was removed after copying the source artifact; no production behavior was changed for the probe.

The [actual resolver](../../../core/schema/directory.go#L424) calls `SnapshotManager.Scratch`, publishes path `/` and its ref, and records no producer. [Normalization](../../../core/value_transfer.go#L47) converts the snapshot to pending with producer state `none`. [Directory routing](../../../core/part_routes.go#L58) returns no producer when `LazyKind` is empty. Merely having the canonical snapshot in B's storage does not supply an eligible local result to the source scan.

## Decision options

1. **Record a narrowly scoped scratch Directory producer (recommended).** Specify a typed no-input producer for successful eager `Query.directory`, using the existing completed-recipe recorder, codec/visitor and fresh private invoker machinery. Its body obtains B's canonical scratch ref; ordinary eager creation stays eager. Specify capture, native restore, independent ownership and cold controls in a binding producer addendum. This extends producer coverage beyond the two Container mounts without inferring a recipe from a recorded field.
2. **Specify an explicit intrinsic-scratch representation and acquisition rule.** Mark canonical empty-directory identity at its producing boundary, preserve it through normalization/relocation, and define how acquisition obtains an independent local canonical ref under the existing gate and lease protocol. This avoids a producer body but changes the persisted/transfer and routing contracts; `/` plus a missing recipe is not enough to identify an empty directory.

Prewarming B, exporting an extra scratch/runtime chain, rerunning schema callbacks from recorded fields, or treating every producer-less Directory as scratch would change or conceal the required cold proof. None was done. The commission remains incomplete pending a binding decision on this newly reached case.
