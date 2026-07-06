# wcprof OTel skip fix code review - Codex

Reviewed commit `c18b17fc53` in
`/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe`
against base `4585bf413d`.

Targeted tests run:

```sh
go test ./core ./dagql -run 'ProfileSkip|Skip|Singleflight'
```

Result: pass.

## Verdict

Not landable as-is. The cache-layer skip is mostly implemented cleanly, but one
load-bearing v2 claim does not compose with the unchanged OTel loader: directly
called reflection accessors keep their ordinary `dag.call` spans, and the loader
still converts any span with `dag.digest` into a wcprof `call` op. Native skips the
same call. That reintroduces skipped reflection-class work into the OTel graph and
breaks native-vs-OTel parity for that case.

Everything else I audited is either correct, acceptable, or a test gap rather than
a code bug.

## REAL Issues

### HIGH - Directly-called reflection `dag.call` spans still enter the OTel wcprof graph

The implementation intentionally separates profiler skip from normal/UI telemetry.
`core.AroundFunc` stamps `ProfileSkip`, but unless the context is already skipped
or `introspectionInfo` suppresses the call, it proceeds to build the normal
telemetry span with `dag.digest`/`dag.call` attributes:

- [core/telemetry.go:40](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:40) stamps `req.ResultCall.ProfileSkip`.
- [core/telemetry.go:41](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:41) only returns early for inherited `dagql.IsSkipped`.
- [core/telemetry.go:44](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:44) only suppresses the normal span when `introspectionInfo` says so.
- [core/telemetry.go:92](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:92) adds `dag.digest` and `dag.call`.
- [core/telemetry.go:444](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/telemetry.go:444) explicitly documents the intended surviving case: a directly-called reflection accessor keeps its normal `dag.call` span while wcprof skips `call_exec`.

Native does skip the wcprof call op for that frame:

- [dagql/cache.go:3605](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3605)
- [dagql/cache.go:3610](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3610)

The OTel loader, however, is unchanged and has no skip signal. It indexes every
span ID, then classifies every span with `dag.digest` as a wcprof `call`:

- [engine/wcprof/wcotel/loader.go:251](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/loader.go:251)
- [engine/wcprof/wcotel/loader.go:444](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/engine/wcprof/wcotel/loader.go:444)

So for the exact case the comment calls out, native has no op, but OTel still has
a `call` op. That makes the "refinement-3 directly-called-accessor divergence=0"
claim dependent on the live workload not exercising the case, not on the code.
It also means "zero loader/replay change" is not sufficient unless normal spans
are suppressed too.

Clean fixes, all emit-side/no-inference compatible:

1. Suppress the ordinary telemetry span too when `ProfileSkip` is true. This
   aligns the recorded data with the skip cut and keeps the loader untouched, but
   gives up the direct-accessor UI span.
2. Add an explicit emitted attribute to the ordinary span, e.g.
   `wcprof.profile_skip=true`, and teach the loader to ignore those spans. That is
   still emit-not-inference, but it is no longer "zero loader change."
3. Revisit the split between UI suppression and profiler suppression; as written,
   keeping a normal `dag.call` span is observably not just UI, because the current
   wcotel loader treats it as profiling data.

This needs a regression test that records a directly-called reflection accessor
outside an inherited skip context, compiles it with `wcotel.Compile`, and proves it
does not produce a wcprof call op after the chosen fix.

### MEDIUM - Load-bearing skip invariants are under-tested

The implemented code paths look right, but some of the exact failure modes from
the design review are not pinned by unit tests.

Missing or weak coverage:

- Lazy skip is not directly exercised. The code gates lazy joiner waits on the
  producer frame via `shared.profileSkip()` at [dagql/cache.go:2993](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:2993), gates lazy op/span minting at [dagql/cache.go:3036](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3036) and [dagql/cache.go:3057](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3057), and gates the leader wait at [dagql/cache.go:3138](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3138). That should be tested with a skipped producer and a non-skipped forcer.
- The "distinct from invalid" invariant is not directly tested. For a non-skipped
  target with an invalid span context, [dagql/cache.go:4013](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4013) still calls `EmitOTelWait`, so the gate should fail loud. Add a test so a future "simplification" does not swallow genuine mixed-recording loss.
- `TestProfileSkipStaticCutSingleflightJoiner` proves the skipped-claimer/skipped-joiner path stays structurally clean, but not the impossible-by-construction opposite-flag case. That is acceptable only if the direct `dag.call` issue above is fixed and the static predicate remains the only way to set the bit.

I would not block merge solely on these tests if the HIGH issue is fixed and live
§9 remains green, but these are the tests that would catch regressions in the
parts most likely to be edited later.

## NOISE / Verified OK

### Frame-homing is mechanically sound

`ProfileSkip` is stored on `ResultCall` with JSON persistence at
[dagql/result_call_frame.go:188](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/result_call_frame.go:188), copied by `clone()` at
[dagql/result_call_frame.go:223](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/result_call_frame.go:223), and copied by `fork()` at
[dagql/result_call_frame.go:255](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/result_call_frame.go:255). The important store/copy paths I checked use `clone()`, `fork()`, or JSON decode/store, so the "travels with the frame" model holds.

The bit is excluded from the digest paths by construction; the tests cover recipe,
content-preferred, and self digests at
[dagql/result_call_frame_profileskip_test.go:31](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/result_call_frame_profileskip_test.go:31).

### Receiver-type stamping avoids the earlier lock hazard

`ReceiverTypeName` is stamped at the object call site from the already-available
receiver type:

- [dagql/objects.go:599](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/objects.go:599)
- [dagql/call_request.go:20](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/call_request.go:20)

`AroundFunc` then performs only map lookups; it does not do an egraph/cache lookup
under the cache locks. This is the right direction for performance and lock safety.

### Reflection type set looks complete for the audited schema names

The extra `EnumValueTypeDef` entry is real and necessary: the Go type
`EnumMemberTypeDef` reports the legacy schema name `EnumValueTypeDef` at
[core/typedef.go:2167](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/typedef.go:2167).

The other reflection types' `Type()` names match the classifier entries
(`Function`, `FunctionArg`, `TypeDef`, `ObjectTypeDef`, `FieldTypeDef`,
`InterfaceTypeDef`, `ScalarTypeDef`, `ListTypeDef`, `InputTypeDef`,
`EnumTypeDef`). The installed fields in `core/schema/module.go` are schema
metadata/accessors/builders; I did not find a field on those receiver types that
forces container exec, module load, or service work. The explicit name traps are
also right: `FunctionCall`, `FunctionCallArgValue`, and `SourceMap` have their
own schema names at [core/typedef.go:2418](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/typedef.go:2418),
[core/typedef.go:2510](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/typedef.go:2510), and
[core/typedef.go:2552](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/core/typedef.go:2552), and are correctly not in the reflection set.

### Singleflight target-side gating is the right cut

The in-flight `ongoingCall` snapshots the target frame's decision at
[dagql/cache.go:3805](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3805), and both native and OTel waits gate on that snapshot at
[dagql/cache.go:3995](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:3995) and
[dagql/cache.go:4013](/home/sipsma/.tailcall/worktrees/sipsma-dagger-219e244e480a/wcprof-otel-skip-coder-daa3a9d2-d93b8afe/dagql/cache.go:4013). This avoids dangling waits into skipped targets without making the loader infer anything.

### No loader/replay change

The diff touches no `engine/wcprof/wcotel` or `engine/wcprof/wcanalyze` files. That
matches the no-inference/no-analysis-change principle, subject to fixing the HIGH
issue above by either suppressing normal spans or explicitly marking them for the
loader.

### §9 caveats

The no-main-baseline capture is acceptable as supporting evidence, not proof; the
code path directly removes the `call_exec`/`publishResult` amplifier. The
`DroppedSpans` proxy is also acceptable for this merge gate if structural gate
counts stay zero and volume remains below the BSP default queue. Imported/adopted
coverage is mostly by construction through `clone`/`fork`/JSON; I would not require
a separate live imported-cache capture before landing.

The remaining native-on/telemetry-off caveat is real but narrow: because the bit is
stamped from `AroundFunc`, a server without telemetry will not set it. If native
wcprof is expected to be run in that configuration, the stamp needs to move out of
the telemetry hook. If production/dev-engine profiling always has `AroundFunc`
registered, this is not a merge blocker.
