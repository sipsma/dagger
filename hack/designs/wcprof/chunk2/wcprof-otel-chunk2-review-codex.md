# wcprof x OTel Chunk 2 Code Review

Reviewed Chunk 2 commit `f127e5662b` against Chunk 1 base `71b69f1f16`
and upstream base `b442cd2533`.

## Overall Verdict

Chunk 2 is sound enough to build Chunk 3 on. The `call_exec` span is minted and
stashed under `callsMu` before `ongoingCalls` publication, wait links use the
approved waiter-side wire format, and the hardened Chunk 1 loader/gate composes
cleanly with the new emitted shape.

I found no code correctness blocker in the singleflight implementation. The one
real issue is validation strength: the committed cap-stress and oracle fixtures
mostly hand-build the expected OTel JSON shape, so they do not mechanically prove
the actual dagql emitter plus SDK span limits produce that shape. If the manual
empirical augmented-engine runs are retained as review artifacts, this is not a
blocker; otherwise add an emitted-trace regression before relying on the cap
claim.

## REAL Issues

### MEDIUM - The committed cap-stress fixture does not exercise the actual emitter or SDK link cap

The implementation plan asks Chunk 2 to validate many suppressed siblings on the
otlpdump path, with zero dropped links at `LinkCountLimit = 16384`
(`hack/designs/wcprof-otel-impl-plan.md:256` to
`hack/designs/wcprof-otel-impl-plan.md:260`). That is the check that would catch
a bad tracer-provider limit, `AddLink` not retaining links, or otlpdump not
surfacing drops.

The committed stress test is useful, but it is a loader/replay synthetic. It
constructs otlpdump-shaped maps with `otSpan`/`otWait`
(`engine/wcprof/wcotel/chunk2_test.go:34` to
`engine/wcprof/wcotel/chunk2_test.go:88`) and then sets no `droppedLinks`, so the
"zero dropped at 16384" assertion is an input assumption, not an observation from
the SDK/exporter (`engine/wcprof/wcotel/chunk2_test.go:304` to
`engine/wcprof/wcotel/chunk2_test.go:338`). The negative check later manually
sets `dropRecs[3]["droppedLinks"] = 1`, which proves the gate sees the field, not
that the engine cap works (`engine/wcprof/wcotel/chunk2_test.go:353` to
`engine/wcprof/wcotel/chunk2_test.go:364`).

The same pattern applies more broadly: the tests mirror the hook contract, but
they do not call the real hooks in `dagql/otelprof_hooks.go:53` to
`dagql/otelprof_hooks.go:112` or the actual cache wiring in
`dagql/cache.go:3690` to `dagql/cache.go:3717` and `dagql/cache.go:3894` to
`dagql/cache.go:3917`.

Recommended adjustment: add at least one emitted-trace regression using an
in-memory SDK tracer/exporter or captured otlpdump from an augmented dev engine:
one executor, repeated joiners, a suppressed-ancestor fan-in, and enough links to
prove the configured cap path. Feed the exported spans through the Chunk 1
loader/gate and assert `call_exec`, `publishResult`, wait-link count, target
resolution, and dropped counts. Manual empirical results can satisfy the current
review, but the repository does not yet enforce them.

## NOISE / Verified Fine

- **Invariant T is implemented correctly.** The cache miss path holds `callsMu`
  before creating/publishing the `ongoingCall` (`dagql/cache.go:3648` to
  `dagql/cache.go:3652`). It starts the OTel `call_exec` span while still under
  that lock (`dagql/cache.go:3690` to `dagql/cache.go:3693`), stashes its
  `SpanContext` on `oc` before publication (`dagql/cache.go:3703` to
  `dagql/cache.go:3718`), and only then publishes to `ongoingCalls`
  (`dagql/cache.go:3720` to `dagql/cache.go:3722`). Joiners that observe `oc`
  therefore have a valid target.
- **Resolver children and `publishResult` parent under `call_exec`.** The resolver
  runs with `oc.sharedWorkCtx`, derived after `beginOTelCallExec`
  (`dagql/cache.go:3691` to `dagql/cache.go:3694`, `dagql/cache.go:3724` to
  `dagql/cache.go:3727`). `publishResult` starts from
  `context.WithoutCancel(oc.sharedWorkCtx)`, so it keeps the ended `call_exec`
  span as parent (`dagql/cache.go:3972` to `dagql/cache.go:3978`). That matches
  the approved late-child, native-parity diagnostic model.
- **Wait links match the wire format.** `c.wait` selects `call_exec` vs
  `singleflight` reason (`dagql/cache.go:3894` to `dagql/cache.go:3901`), records
  absolute Unix-ns wait bounds (`dagql/cache.go:3909` to
  `dagql/cache.go:3917`), and `emitOTelCallWait` writes
  `link.purpose="wait"`, reason, and decimal-string start/end attrs to a link on
  the current waiter span (`dagql/otelprof_hooks.go:95` to
  `dagql/otelprof_hooks.go:111`).
- **Chunk 1 and Chunk 2 compose.** The current loader counts unresolved non-lock
  wait targets and malformed wait timings (`engine/wcprof/wcotel/loader.go:342`
  to `engine/wcprof/wcotel/loader.go:365`), and the gate fails both, plus dropped
  links/attrs on wait-carrying traces (`engine/wcprof/wcotel/gate.go:118` to
  `engine/wcprof/wcotel/gate.go:130`). That closes the Chunk 1 review holes for
  the new wait links.
- **Omitting `dag.call` on `call_exec`/`publishResult` is justified.** The design
  listed `dag.call` for `call_exec` (`hack/designs/wcprof-otel-design.md:469` to
  `hack/designs/wcprof-otel-design.md:472`), but the loader/oracle only need span
  name plus `dag.digest` and explicit op kind. The hook sets those attrs
  (`dagql/otelprof_hooks.go:53` to `dagql/otelprof_hooks.go:60`) and marks the
  spans passthrough; avoiding another `CallPB().Encode()` on the miss path is a
  reasonable discovered simplification.
- **Always-on telemetry is the right posture for the goal.** The hook is gated on
  an active recording span, not `wcprof.Enabled` (`dagql/otelprof_hooks.go:22` to
  `dagql/otelprof_hooks.go:38`). That is necessary if the artifact of record is a
  Cloud trace from an ordinary run. Volume is bounded to cache misses and waiters:
  cache hits return before the miss/singleflight path (`dagql/cache.go:3638` to
  `dagql/cache.go:3646`), while misses add one `call_exec`, one
  `publishResult`, and one wait link per caller. The product owner should still
  explicitly accept this always-on volume; live export means those extra span IDs
  can be exported as start/end snapshots.
- **The mixed container-workload jaccard=0 claim is expected, not a regression.**
  The implementation plan explicitly says Chunk 2 should converge on
  singleflight-heavy workloads while lazy and exec/service workloads still drift
  until Chunks 3 and 4 (`hack/designs/wcprof-otel-impl-plan.md:278` to
  `hack/designs/wcprof-otel-impl-plan.md:281`). Once `call_exec` exists, the
  loader also intentionally suppresses the un-augmented `withExec => exec`
  fallback in favor of the corrected shape, so exec-heavy rankings can get worse
  before Chunk 4 makes user process work first-class. That is trajectory, not a
  design break.
- **No degenerate performance shape found.** The hot-path OTel work is constant
  per miss/waiter: one span start/end for `call_exec`, one span start/end for
  publication, and one `AddLink` per blocked caller. The SDK link queue is append
  under the configured cap; pathological overflow is the documented >16384 case.
  Starting the span under `callsMu` does invoke `LiveSpanProcessor.OnStart`, but
  that path enqueues a snapshot into a batch processor rather than synchronously
  writing SQLite/Cloud (`github.com/dagger/otel-go` `live.go:25` to `live.go:30`;
  OTel SDK batch processor `OnEnd` enqueues at `batch_span_processor.go:145` to
  `batch_span_processor.go:157`).
- **The `lostcancel` warning is pre-existing.** `go vet ./dagql` reports the same
  warning on Chunk 1 base `71b69f1f16` (`dagql/cache.go:3668`, return at `:3682`)
  and on Chunk 2 (`dagql/cache.go:3674`, return at `:3701`), shifted only by the
  inserted lines.

## Holistic Trajectory

Chunks 1+2 are still aligned with the north star. The loader remains mechanical,
the gate now fails wait-loss cases, and Chunk 2 provides the first real causal
edge source that the unchanged replay can use. The design assumption that mixed
workloads should not fully converge until lazy and exec/service chunks is holding
up; the important signal is that singleflight-heavy shapes now have a concrete
`call_exec` target and per-caller waits.

The remaining big correctness risk is validation coverage, not the model. Before
declaring the cap/Cloud story production-ready, the actual emitted spans need to
be exercised end-to-end, not only synthetic JSON.

## Tests Run

Against detached worktree at `f127e5662b`:

```sh
go test ./dagql ./engine/wcprof/wcotel ./cmd/wcprof-oracle ./cmd/wcprof-otel-analyze
```

Result: pass.

Vet comparison for the reported warning:

```sh
go vet ./dagql
```

Result: fails on both `71b69f1f16` and `f127e5662b` with the same pre-existing
`lostcancel` warning in `dagql/cache.go`.
