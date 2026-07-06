# wcprof x OTel design review, pass 3

Reviewed: `hack/designs/wcprof-otel-design.md` refreshed 1202-line draft.

Overall verdict: **sound enough to build v1 on**, with one real issue in a
reserve/future mechanism. The five pass-2 fixes are now correctly framed for v1,
and the new `exec.run` and hidden lazy passthrough span are additive and consistent
with current source. The one real issue is not in the v1 implementation sequence,
but the doc should not leave the reserve merge described as replay-exact without
narrower preconditions.

## REAL issues

### 1. MEDIUM, reserve-only: same-waiter/same-target wait-link merge is not replay-exact as stated

Verdict: **REAL issue**, but **not a v1 blocker** because this is explicitly held
in reserve, not part of the implementation sequence.

The doc says an emit-side merge of multiple waits from the same waiter to the
same target into one "interval-union link" is replay-exact because repeated joins
to the same target are idempotent `max` operations
(`hack/designs/wcprof-otel-design.md:1145-1153`, also summarized at
`hack/designs/wcprof-otel-design.md:348-350`). That is only conditionally true.
Replay's join operation is idempotent, but waits are not only joins:

- Wait classification is per interval: a wait is `actWaitJoin` only if its target
  exists, is not self, and `wait.EndNS >= target.EndNS - epsilon`; otherwise it
  can be `actWaitNoop` (`engine/wcprof/wcanalyze/replay.go:162-173`).
- Replay executes the action at the wait's start time and then performs
  `clock = max(clock, finish(target))` for `actWaitJoin`
  (`engine/wcprof/wcanalyze/replay.go:371-381`).
- The same wait intervals are also removed from the waiter's self-time
  (`engine/wcprof/wcanalyze/graph.go:379-398`).
- The v1 wait-link wire shape has a single `wcprof.wait.start_unix_ns` and
  `wcprof.wait.end_unix_ns`, not a multi-interval payload
  (`hack/designs/wcprof-otel-design.md:299-304`).

So collapsing arbitrary same-waiter/same-target waits to one `[min(start),
max(end)]` link can change both the action schedule and self-time. A concrete bad
case: an early abandoned wait to target `T` ends before `T.EndNS` and is therefore
`actWaitNoop`; later the same waiter really joins `T`. A single merged interval
ending at the later join would classify as `actWaitJoin` at the early start,
serializing too early and deleting the self-time between the two waits. Even when
all waits classify as joins, disjoint intervals lose the self-time gap between
them unless the encoding preserves a true disjoint interval union.

This reserve is safe if narrowed to one of these forms:

- merge only overlapping/adjacent intervals whose individual replay
  classification is identical and whose merged interval has the same action
  point semantics; or
- encode a true multi-interval union and expand it back to multiple wait events
  before replay; or
- keep one link per disjoint interval and only coalesce duplicate/overlapping
  links.

The common high-fan-in shape may well satisfy the overlapping-join condition, but
the doc currently states a general property that the replay contract does not
provide.

## Verified fixes / noise

### 1. Absolute Unix-ns decimal-string wait attrs

Verdict: **NOISE; fixed correctly.**

The revised wire format is now emitter-implementable. The engine can know
absolute wall-clock time at the wait sites, while trace-min-start is only known
after ingest; the doc now says the loader rebases wait attrs to the same epoch as
span start/end (`hack/designs/wcprof-otel-design.md:307-327`,
`hack/designs/wcprof-otel-design.md:852-857`). That matches the Cloud conversion
path: span timestamps are typed `time.Time` fields
(`internal/cloud/trace.go:81-94`) and convert back to OTLP Unix nanos
(`internal/cloud/trace.go:305-324`), while link attributes are a
`map[string]any` (`internal/cloud/trace.go:112-117`) where JSON numbers go through
`float64` but strings remain strings (`internal/cloud/trace.go:368-379`).

The string approach therefore avoids the attr precision loss without trying to
emit trace-relative offsets that the engine cannot know.

### 2. Suppressed-parent link fan-in and `LinkCountLimit = 16384`

Verdict: **NOISE for v1; the hard cap is an explicit project decision.**

The doc no longer relies on the false "handful of links per span" premise. Current
DagQL really does fan sibling selections concurrently
(`dagql/server.go:1120-1163`), and suppressed repeated calls really can place
their links on an ancestor because repeated telemetry is suppressed by
`ShouldEmitTelemetry` (`dagql/telemetry.go:48-64`,
`core/telemetry.go:53-64`).

The SDK behavior is also as described: the default link cap is 128
(`go.opentelemetry.io/otel/sdk@v1.43.0/trace/span_limits.go:61-68`,
`span_limits.go:102-104`), link overflow evicts the oldest link
(`go.opentelemetry.io/otel/sdk@v1.43.0/trace/evictedqueue.go:38-55`), and the
dropped count exists locally on `ReadOnlySpan`
(`go.opentelemetry.io/otel/sdk@v1.43.0/trace/snapshot.go:117-120`) but not in the
Cloud API's link shape (`internal/cloud/trace.go:112-117`). Setting a higher
provider limit is supported (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/provider.go:421-443`).

The 16384 value is not a proof of impossibility; it is a bounded operating limit.
That is fine because the doc now says exactly that, stress-tests below the cap,
and requires a Cloud round-trip at thousands-scale fan-in
(`hack/designs/wcprof-otel-design.md:334-350`,
`hack/designs/wcprof-otel-design.md:978-988`,
`hack/designs/wcprof-otel-design.md:1007-1026`). The only correction needed is the
reserve merge claim above.

### 3. `dagql.publishResult` attribution

Verdict: **NOISE; fixed correctly.**

The doc now frames `dagql.publishResult` as native-parity diagnostics, not a
counterfactual attribution fix. That matches the current native shape:
`publishResult` is started under `oc.profOpID`
(`dagql/cache.go:3922-3934`) around `initCompletedResult`
(`dagql/cache.go:3936-3943`), but the shared `call_exec` already ended in the
resolver goroutine (`dagql/cache.go:3700-3706`) and the caller's initial wait
closed before publication (`dagql/cache.go:3875-3881`). Replay only implicitly
joins children up to the parent's own end time
(`engine/wcprof/wcanalyze/replay.go:353-369`,
`engine/wcprof/wcanalyze/replay.go:389`).

So the current late-child shape preserves a diagnostic row and native/OTel table
parity, while publication wall time remains absorbed by the caller/ancestor
class. The §9 seam to make publication a real wait target in both sources if it
proves hot is the right disposition.

### 4. Lazy UI-visible structure and hidden passthrough span

Verdict: **NOISE; the softened claim is correct for normal dagui views.**

The doc no longer promises byte-for-byte trace identity; it promises unchanged
UI-visible structure (`hack/designs/wcprof-otel-design.md:574-580`). Current
source supports the new hidden passthrough case:

- `telemetry.Passthrough()` sets `dagger.io/ui.passthrough`
  (`github.com/dagger/otel-go@v1.43.1-0.20260515012101-af7cd0684887/span.go:46-49`),
  whose attr docs say the span is substituted for its children
  (`github.com/dagger/otel-go@v1.43.1-0.20260515012101-af7cd0684887/attrs.go:73-74`).
- dagui processes that attr into `SpanSnapshot.Passthrough`
  (`dagql/dagui/spans.go:386-387`).
- normal tree walking skips passthrough spans and walks their children with the
  same displayed parent (`dagql/dagui/types.go:139-148`), and child counts recurse
  through passthrough spans (`dagql/dagui/spans.go:218-227`).

The producer-context case is also consistent with current lazy behavior: native
creates the lazy op before publishing `lazyEvalWaitCh`
(`dagql/cache.go:2957-2962`), current OTel creates the hidden resume span inside
the goroutine (`dagql/cache.go:2984-2990`), and current tests assert the re-pointed
work's parent is the original producer while the resume span is a passthrough child
of the trigger (`dagql/cache_test.go:544-550`). Minting that resume span earlier
and ending it in the goroutine changes timing but not the visible parentage.

One small wording precision: debug dagui views may still show passthrough spans,
because the skip is guarded by `!opts.Debug` (`dagql/dagui/types.go:139`). That is
not a design defect; the user-facing non-debug tree is what the doc is preserving.

### 5. Persisted/imported cache-hit residual

Verdict: **NOISE; fixed correctly.**

The new framing is accurate. Native records a per-call wcprof op before entering
`getOrInitCallInner`, including cache hits (`dagql/cache.go:3505-3518`). OTel
suppresses repeated cacheable call spans via `ShouldEmitTelemetry`
(`dagql/telemetry.go:48-64`, used at `core/telemetry.go:53-64`).

The residual path is real but shared: `ensurePersistedHitValueLoaded` can wait on
`attachDepsWaitCh` or `persistDecodeWaitCh`
(`dagql/cache_persistence_import.go:563-620`), and there is no
`wcprof.BeginWait` in that function. So the gap is not "OTel wrong vs native
right"; it is a bounded native/OTel attribution difference caused by native still
having a suppressed-repeat call op while OTel folds the time into the ancestor.
The fixture gate and "fix both sources with a real decode target if hot" seam are
the right way to keep the oracle meaningful
(`hack/designs/wcprof-otel-design.md:783-799`,
`hack/designs/wcprof-otel-design.md:989-997`).

### 6. Added `exec.run` span

Verdict: **NOISE; clean additive wrapper.**

Native already has an `OpKindExec` `exec.run` op with nested-client stitching
metadata (`engine/engineutil/executor.go:116-130`) and setup phases under it
(`engine/engineutil/executor.go:188-203`). Native also records the
`exec.containerStart` / `exec.processRun` split at the started-callback boundary,
with user work only on `exec.processRun`
(`engine/engineutil/executor_spec.go:1398-1412`).

The proposed OTel parentage is coherent: the executor is invoked inside the
`withExec` resolver's `call_exec`, while nested-client propagation uses
`causeCtx` captured before any new `exec.run` span exists
(`core/container_exec.go:1304`,
`engine/engineutil/executor_spec.go:751-753`,
`engine/engineutil/executor_spec.go:835-837`). That makes nested-client work a
sibling of `exec.run` under `call_exec`, not a child of `exec.run`; the doc now
states that explicitly (`hack/designs/wcprof-otel-design.md:690-703`). For replay,
the `call_exec` implicit join waits for both siblings, so this does not introduce
false serialization or a cycle. It also does not break propagation because the
propagated context is intentionally the pre-existing causal `withExec` context.

The wrapper may add telemetry visible in debug or detailed trace views, but unlike
lazy parentage the design never made "no UI delta" an invariant for exec phases.

## Full-pass invariants

### Invariant E / replay reuse

Verdict: **v1 passes.**

The emitted v1 graph shape matches replay's actual contract: nested children are
implicit joins; explicit waits are on the waiter and become `actWaitJoin` only when
the target actually ran through the wait end
(`engine/wcprof/wcanalyze/replay.go:162-173`,
`engine/wcprof/wcanalyze/replay.go:371-389`). Suppressed caller waits landing on
an ancestor are not inherently over-serializing because repeated joins to the same
target use `max`, not sum, and self-time subtracts wait interval unions
(`engine/wcprof/wcanalyze/replay.go:379-381`,
`engine/wcprof/wcanalyze/graph.go:379-398`). The reserve merge issue is the only
place the doc overextends that idempotence.

### Anti-inference / faithful emit

Verdict: **passes.**

The loader's causal decisions are now all emitted data: `wcprof.op.kind`,
`wcprof.parent` when present, `parentId` otherwise, and `purpose=wait` links with
target ids and explicit intervals (`hack/designs/wcprof-otel-design.md:828-865`).
The mechanical loader steps still include dedup/classification/epoch choice, but
none of those infer causality from timestamps, `dag.inputs`, non-wait links, or
overlap. The `wcprof.parent ?? parentId` rule remains emit-not-inference as long
as the stamping processor is installed on every provider that can create lazy work;
the doc now keeps that as a residual implementation risk and fixture
(`hack/designs/wcprof-otel-design.md:1168-1177`).

### Volume and OTel limits

Verdict: **passes with a documented hard limit.**

The design avoids per-joiner spans and keeps the new v1 volume to executed-call
support spans plus wait links. The link-cap path is now explicit and testable. The
remaining >16384 suppressed-sibling fan-out is a human/product hard limit, not a
hidden correctness assumption.

### Validation plan

Verdict: **strong enough for v1.**

The structural gates cover cycles, dropped links on the local path, orphan waits,
lazy re-parenting, no double-count checks, suppressed-sibling semantics/cap stress,
persisted-cache drift, exec setup-vs-runtime attribution, and Cloud round-trip
durability (`hack/designs/wcprof-otel-design.md:901-1026`). The Cloud round-trip is
the right answer to the "do link attrs survive engine -> client -> Cloud -> API"
question because the Cloud API itself has no dropped-link field
(`internal/cloud/trace.go:112-117`). I would size at least one Cloud fixture as
close to the configured cap as Cloud/reliability budgets allow, but the current
"thousands, beyond realistic max" requirement is a reasonable v1 gate.

### Scope: one trace

Verdict: **passes.**

The design no longer tries to aggregate a whole CI job across traces, and I did
not find a v1 loader requirement that depends on multiple trace ids. Multi-engine
or scale-out within one trace remains a validation seam, not forbidden cross-trace
inference (`hack/designs/wcprof-otel-design.md:1154-1162`,
`hack/designs/wcprof-otel-design.md:1195-1202`).

## Human decisions

- `LinkCountLimit = 16384` is an explicit bounded operating decision. The design
  must test and document it; it cannot prove no user will exceed it.
- Lazy producer-side rendering is out of scope to change. The causal-parent
  override preserves normal dagui structure while giving the analyzer emitted
  causal parentage.
- One Cloud trace is the scope. Whole-CI aggregation would require cross-trace
  correlation and is correctly out of scope for this anti-inference loader.

## Build recommendation

Proceed with v1 implementation after fixing or qualifying the reserve merge text.
Do not implement that merge from the current wording. Everything else reviewed in
this pass is consistent with current source and the replay contract.
