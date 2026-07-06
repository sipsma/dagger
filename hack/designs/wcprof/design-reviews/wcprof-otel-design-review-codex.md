# wcprof x OTel design review

## Overall verdict

The design is directionally right on the central hard problem: OTel cannot be
fed to `wcanalyze` as a raw span tree, and the proposed `call_exec` span plus
explicit waiter-side wait edges is the right shape for dagql singleflight. The
lazy-eval section also identifies the real current lie: child spans are
intentionally re-pointed to the producer span via `resumedCallbackSpan`, which
is incompatible with wcprof's synchronous-parent assumption.

I would not call the design sound enough to build as written. It handles the
dagql singleflight/lazy crux but drops or mis-models several native wcprof hook
points that are part of the stated goal, especially container exec/user-work
attribution, session phases, service availability vs start, cache-volume locks,
and result publication. Those are not reporting polish; they can change
counterfactual rankings. The validation plan also relies on detecting dropped
wait links in Cloud traces, but the current Cloud trace API model does not carry
`DroppedLinksCount`.

## Ranked real issues

### P0: The exec/user-work model is wrong and would mis-rank real bottlenecks

The design says OTel can tag the `withExec`/exec span as `wcprof.work_type =
"user"` because "a leaf exec's user-process time shows up as the `withExec`
span's self-time" and executor phases are "sub-ms engine overhead." That is not
what native wcprof records, and it contradicts PR #13393's validation.

Evidence:

- Native records one `exec` op and one `exec_phase` op per setup phase in
  `engine/engineutil/executor.go:116-150` and `engine/engineutil/executor.go:188-198`.
- Native explicitly splits container startup from user process runtime:
  `exec.containerStart` and `exec.processRun` at
  `engine/engineutil/executor_spec.go:1398-1412`, with `exec.processRun`
  tagged `WorkTypeUser`.
- Native records `withExec.prepareMounts` and `withExec.applyOutputs` around
  mount/output work at `core/container_exec.go:1586-1743` and
  `core/container_exec.go:2175-2183`.
- The merged wcprof README describes these as current hook points, not
  optional decoration: `engine/wcprof/README.md:36-41`.
- Existing OTel does not create equivalent executor phase spans; the executor
  mostly emits events such as "Container started" on the active span
  (`engine/engineutil/executor_spec.go:1270-1277`) rather than phase spans.

Impact:

If OTel just labels `withExec`/the broad exec span as user work, engine setup,
mount preparation, output commit, and process runtime all collapse into one
class. That can report "user code is slow" when the actionable bottleneck is
engine setup. PR #13393 explicitly found `exec.setupNetwork` as a high-tail
engine bottleneck; treating executor phases as sub-ms would have hidden that
class. This violates the brief's "user work first-class" goal because "first
class" requires separating user process time from engine overhead, not painting
the whole exec interval as user-controlled.

Required fix:

The OTel design needs an equivalent to native's `exec.run`, setup phase spans,
and `exec.containerStart` / `exec.processRun` split, or it needs to explicitly
scope the feature to a less faithful first version and accept wrong user-vs-
engine rankings. The current plan does neither.

### P1: Service availability is not representable by the proposed "one span -> one op" loader

The design correctly says long-lived service availability must not count as
blocking work, but then says the loader emits one op per span. The existing
service OTel span is a long-lived service exec span, and the native wcprof op is
only the start/health-check interval.

Evidence:

- Native stores a `profOpID` on `startingService`, and waiters wait on that
  start op: `core/services.go:47-58`, `core/services.go:955-963`.
- The native `service_start` op is started immediately before `svc.Start` and
  ended after start/health-check completes or fails:
  `core/services.go:971-1026`.
- The service exec OTel span is started in `core/service.go:748-754`, stored on
  `RunningService` at `core/services.go:250-276`, and intentionally lives until
  service exit on success (`core/service.go:765-770` only ends it immediately
  on start error).
- `wcanalyze` has no "availability marker with zero self-time" concept. It
  subtracts child and wait intervals from an op's interval
  (`engine/wcprof/wcanalyze/graph.go:381-398`) and replay treats structural
  children as spawn/join candidates (`engine/wcprof/wcanalyze/replay.go:154-173`,
  `engine/wcprof/wcanalyze/replay.go:353-369`).

Impact:

If the long-lived service exec span becomes an op, it can either leak idle
availability time into analysis, become an unreached child, or distort parent
self-time by subtracting an async child interval. If it is omitted, the design
must say so despite "one op per span." The current text asks for "mark the idle
remainder so it contributes no self-time," but the target IR cannot express
that.

Required fix:

Emit and store a dedicated `service.start`/`service_start` OTel span context on
`startingService`, use that as the wait target, and explicitly omit the
long-lived service availability span from the wcprof runtime graph (or add a
separate non-runtime graph concept outside `wcanalyze`). Do not pretend the
existing service span can be mechanically loaded as a normal op.

### P1: Wait-link loss is not detectably safe through Cloud ingest

The wait-edge wire format depends on span links. The design says the loader will
assert no links were dropped by counting links and erroring if any span is at
the limit. That is not a sound check, and the production Cloud API path appears
to drop the explicit dropped-link count.

Evidence:

- The Go OTel SDK default `LinkCountLimit` is 128 and drops the oldest link once
  the limit is reached (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/span_limits.go:21-31`,
  `:61-68`).
- `recordingSpan.AddLink` silently ignores links after the span stops recording
  and exposes only `DroppedLinks()` as the reliable loss signal
  (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/span.go:764-808`).
- The engine's local client DB stores `DroppedLinksCount`
  (`engine/server/telemetry.go:354-391`) and `clientdb` can read it back
  (`engine/clientdb/span.go:138-171`).
- The Cloud trace API model used by `internal/cloud/trace.go` includes `links`
  but no dropped-link count (`internal/cloud/trace.go:81-99`,
  `internal/cloud/trace.go:112-117`), and `SpansToPB` reconstructs links without
  any dropped count (`internal/cloud/trace.go:349-363`).
- The per-client tracer provider in `engine/server/session.go:684-727` does not
  currently configure `WithSpanLimits`, so the default applies unless changed.

Impact:

A missing wait link is not a cosmetic data loss; it changes self-time and
counterfactual propagation. Counting `len(span.Links()) == limit` is both a
false positive when exactly at the limit and a false negative if the effective
limit is unknown or if Cloud already truncated upstream. For Cloud ingest, the
loader cannot assert the loss signal because the API model does not expose it.

Required fix:

Carry dropped-link counts through Cloud trace ingest, or choose a wait-edge
encoding whose loss signal survives. Also configure explicit span limits on the
engine/client tracer providers and test that the final Cloud trace API payload
preserves wait-link attributes and dropped counts.

### P1: Session phases and attachables wait are omitted, so "why was my run slow?" can become root self-time

The design treats `POST /query` as the natural root and says missing wcprof
session phases would show up as root self-time / dead-air. That is a regression
from native and weakens the stated Cloud/CI goal.

Evidence:

- Native creates `session.serveQuery`, wraps the request in profiling context,
  and records an attachables wait:
  `engine/server/session.go:1441-1459`.
- Native records workspace load, module load, schema build, and query phases:
  `engine/server/session.go:1478-1517`.
- The wcprof README lists these phase ops as current hook points:
  `engine/wcprof/README.md:42-44`.
- The OTel `POST /query` span is a passthrough wrapper only:
  `engine/server/session.go:1406-1425`.

Impact:

CI slowness often happens before the root GraphQL field actually runs: waiting
for attachables, loading workspace/modules, building schema, or initializing a
session. Collapsing that into a generic root op makes the headline answer less
actionable and prevents cross-source oracle equivalence with native.

Required fix:

Emit OTel spans or wait links for the native session phases, at least
`session.serveQuery`, `session:attachables`, `session.workspaceLoad`,
`session.modulesLoad`, `session.schemaBuild`, and `session.query`.

### P2: Cache-volume locks and result publication are dropped despite being native wait/self-time choke points

The design includes `lock` in the wait reason vocabulary but never plans the
actual cache-volume lock wait emit. It also mentions `dagql.publishResult` only
indirectly in native context and does not emit an OTel equivalent.

Evidence:

- Cache-volume lock waits are recorded as named resource waits:
  `core/container_exec.go:593-600`.
- Result publication is explicitly parented under the shared `call_exec` op:
  `dagql/cache.go:3922-3943`.
- The wcprof README lists both as current hook points:
  `engine/wcprof/README.md:28-32` and `engine/wcprof/README.md:40-41`.
- Replay treats resource waits as fixed delays and target waits as joins
  (`engine/wcprof/wcanalyze/replay.go:162-173`), so missing either kind changes
  self-time and can change rankings.

Impact:

Lock contention and publish/indexing contention can be true critical-path
bottlenecks. If they are omitted, their time is folded into broader parent
self-time, making the "what would I fix" answer less precise or wrong.

Required fix:

Add explicit OTel wait links for cache-volume lock acquisition and an internal
`dagql.publishResult` span/op under `call_exec`, or explicitly document that the
OTel source is less faithful than native and exclude these classes from oracle
comparison. The latter would contradict the brief.

### P2: The loader classification rules are under-specified and one stated rule is wrong for joiners

The loader says `Kind` is inferred from attributes, including "a span with
`dag.digest` and child `call_exec` => call." That misses visible singleflight
joiner call spans: they have `dag.digest`/`dag.call` and a wait link to a
`call_exec`, but the execution is not their child.

Evidence:

- Current `AroundFunc` puts `dag.digest` and `dag.call` on the call span:
  `core/telemetry.go:83-86`.
- Singleflight joiners return through `c.wait` on an existing `ongoingCall`
  (`dagql/cache.go:3647-3657`, `dagql/cache.go:3853-3881`); their target
  execution is in another subtree by design.
- `wcanalyze` grouping is by `Kind` + `Class`
  (`engine/wcprof/wcanalyze/graph.go:83-89`), so bad kind classification changes
  report grouping and what-if candidates.

Impact:

This is not causal inference, but it is still load-bearing. A joiner call span
must classify as `call` even without a child `call_exec`. The design should use
explicit `wcprof.op.kind` wherever possible and simple attribute classification
(`dag.call`/`dag.digest` => call) otherwise.

### P2: Cloud trace clock-domain handling is acknowledged but not designed

The design notes clock skew across clients as an open question, but production
ingest is explicitly the Cloud trace API. The replay runs in one timestamp frame
and PR #13393 notes that frame mixing was a real replay bug class. Leaving this
as a seam is risky for CI traces containing client, engine, nested-client, and
possibly container-emitted spans.

Evidence:

- Replay intentionally stays in one original time frame because mixing frames
  corrupts schedules (`engine/wcprof/wcanalyze/replay.go:20-27`).
- Roots are chained by original timestamps (`engine/wcprof/wcanalyze/replay.go:265-297`).
- Cloud span data carries absolute timestamps from whatever process emitted the
  span (`internal/cloud/trace.go:81-99`).

Impact:

If the loader includes spans from multiple clock domains, a child can appear
outside its parent, waits can become `actWaitNoop` because end times do not line
up, and roots can be chained incorrectly. This can directly change baseline and
what-if makespans.

Required fix:

Specify the initial production scope. Either load only engine-clock spans for
the wcprof graph, or define a clock-normalization mechanism that is not causal
inference and validate it against corrected traces. Do not leave this to a later
Cloud-ingest step.

## Important NOISE / acceptable claims

### NOISE: Reusing `Build` and replay unchanged is correct

The design's "compile OTel to `DumpEvent`/`DumpHeader`, then call `Build`" target
is right. `Build` already attaches waits, wires structural parents, handles
native nested-client links, and computes self-time
(`engine/wcprof/wcanalyze/graph.go:247-301`,
`engine/wcprof/wcanalyze/graph.go:381-398`). Replay's contract is exactly the
one the OTel emitter must satisfy: self segments, child spawns, explicit waits,
and implicit joins (`engine/wcprof/wcanalyze/replay.go:154-173`,
`engine/wcprof/wcanalyze/replay.go:353-385`).

### NOISE: The singleflight `call_exec` + waiter-side wait-edge model is the right core fix

Native does record one caller op per call, one shared execution op, and waits
from callers to that execution (`dagql/cache.go:3515-3530`,
`dagql/cache.go:3669-3717`, `dagql/cache.go:3867-3881`). Current OTel call
spans are suppressed by digest (`dagql/telemetry.go:48-64`) and current
`AroundFunc` returns the unchanged context when suppressed
(`core/telemetry.go:58-64`), so a suppression-independent execution span is
needed.

Putting links on waiters rather than fanning joiner links into the target is
also right. Replay consumes waits from the waiter timeline; fan-in on the target
would be the wrong direction and would hit link caps earlier.

### NOISE, with one implementation caveat: suppressed joiner waits can attach to the parent/ancestor

The design's answer to "what if the joiner call span was suppressed?" is mostly
sound: the cache layer should emit the wait from the current recording span in
`ctx`, which is the actual live caller/ancestor span. For concurrent child
resolution, multiple overlapping wait links on that ancestor do not inherently
serialize in replay: `actWaitJoin` uses `clock = max(clock, targetFinish)`
(`engine/wcprof/wcanalyze/replay.go:379-381`), not `clock += wait`.

The caveat is implementation-specific: `trace.Span.AddLink` is a no-op after a
span ends or on a non-recording span (`go.opentelemetry.io/otel/sdk@v1.43.0/trace/span.go:764-777`).
The emitter must add the wait link before the waiter span ends and must have a
real recording span in context. This is testable with `otlpdump`.

### NOISE: Cache hits can remain suppressed for the runtime wait graph

For the counterfactual wall-clock source, dropping fully satisfied cache-hit
call spans is acceptable if pending-lazy hits are still handled. Current
telemetry already avoids marking pending lazy results as cached
(`core/telemetry.go:257-261`), and lazy evaluation has a separate wait/execution
path (`dagql/cache.go:2935-3058`). Cache-hit structure matters for the future
cache-diff graph, not for runtime waits unless a hit still has deferred work.

### NOISE: Ignoring `dag.inputs` and existing cause/error links is correct

`dag.inputs` is emitted as call metadata in `core/telemetry.go:119-127`; it is a
cache-key/input edge, not a runtime wait edge. Existing link purposes are
`cause` and `error_origin` in `github.com/dagger/otel-go` attrs
(`attrs.go:92-100` in the vendored module). The loader should ignore those for
wcprof runtime causality.

### NOISE: The lazy-eval critique is real, and the proposed direction is broadly right

Current lazy telemetry deliberately creates a resume span under the triggering
consumer but then wraps the callback in a `resumedCallbackSpan` whose
`SpanContext` returns the original producer context
(`dagql/cache.go:2797-2808`, `dagql/cache.go:2968-2995`). Tests assert that
lazy child spans and logs attach to the original span while the resume span is
under the trigger (`dagql/cache_test.go:495-555`). That is useful for dagui
failure attribution but false for wcprof's synchronous-parent model.

The design is right that this needs a UI-owner decision. For wcprof, the lazy
work container needs to be the consumer-triggered resume/lazy op, and other
consumers need explicit waits to that op (`dagql/cache.go:2935-2943`,
`dagql/cache.go:3055-3058`). Keeping producer-side UI associations as
non-runtime links is fine; treating them as parentage or waits is not.

### NOISE: OTel nested-client stitching is plausibly simpler than native

Native wcprof needs a `nested_client` link because wcprof context cannot cross
the container boundary (`engine/engineutil/executor.go:126-130`). OTel does
propagate the exec/cause context into the container environment
(`engine/engineutil/executor_spec.go:745-753`,
`engine/engineutil/executor_spec.go:835-837`), so nested-client API spans can
arrive already parented under the exec/cause span. This is acceptable, provided
the corrected exec span exists and the Cloud trace API preserves that parentage
in the real production path.

## Invariant verdicts

- **Anti-inference: mostly NOISE, with loader-classification caveats.** The
  design's wait-link-only causal path honors the brief: do not turn
  `dag.inputs`, cause links, timestamp overlap, or parentless spans into waits.
  The wrong joiner kind rule above is classification, not causal inference, but
  it still needs fixing. Any future clock normalization must not become
  timestamp-containment reparenting in disguise.
- **Faithful emit / no replay surgery: REAL gaps remain.** The design correctly
  refuses replay changes and treats cycles as bad emit. But it has not yet made
  service spans, exec/user work, session phases, locks, or publishResult
  faithful, so "cycles are impossible by construction" is not earned yet.
- **Two graphs / one IR: NOISE.** The runtime wait graph and cache-key input
  graph are kept separate. Ignoring `dag.inputs` for runtime waits is correct.
- **Respect volume: mixed.** One `call_exec` span per executed call plus
  waiter-side links is a reasonable volume tradeoff. The design over-applies the
  volume argument to exec/session/service hooks: native's phase spans are few
  and are exactly where several real bottleneck classes live.
- **Wait-edge attributes survive export: partially verified, not complete.**
  Local engine/client storage preserves links and link attributes
  (`engine/server/telemetry.go:354-391`, `engine/clientdb/span.go:138-144`),
  and the Cloud trace API model includes link attributes
  (`internal/cloud/trace.go:112-117`, `internal/cloud/trace.go:349-363`). The
  missing piece is dropped-link/loss metadata, which is a real issue above.
- **No replay surgery / validation on corrected traces only: NOISE.** The plan
  correctly says not to tune from uncorrected traces and centers a corrected
  native-vs-OTel oracle. The oracle just needs the missing native hook families
  included before it can be meaningful.

## Validation review

The validation plan has the right centerpiece: a cross-source oracle comparing
native wcprof and OTel-compiled graphs on the same corrected engine run. That is
the only credible way to prove the OTel emitter is faithful.

The plan needs tightening before implementation:

- The oracle must compare every native hook family, not just dagql
  singleflight/lazy: session phases, cache locks, publishResult, exec phases,
  `exec.processRun`, service starts.
- It must run after the OTel source includes those hook families. Comparing
  against native while intentionally omitting them will either fail noisily or
  train people to loosen the oracle.
- The dropped-link gate must use an explicit dropped-link count. Cloud ingest
  currently does not expose one.
- Add known-answer tests for `exec.setupNetwork`/container-start vs
  `exec.processRun`, not only `withExec sleep`; otherwise the main
  user-vs-engine attribution bug above will pass.
- Add service tests where the service remains alive after start; assert idle
  lifetime contributes zero to wcprof self-time and does not become a root
  makespan driver.
- Add a session-phase test where module/schema load dominates before
  `session.query`; assert it ranks as a session phase rather than root dead-air.

## Human decisions required

1. Whether OTel wcprof must match native hook coverage before being considered
   usable. My recommendation: yes, for the first Cloud-facing version.
2. Whether changing lazy telemetry parentage is acceptable for dagui. My
   recommendation: split runtime parentage from UI association; keep producer
   association as a non-runtime link.
3. Whether Cloud trace API can be extended to include dropped-link counts and
   any other span-limit loss signals. Without that, wait links are not a safe
   production dependency.
4. Whether the OTel loader should scope initially to engine-clock spans only.
   My recommendation: do that first unless a non-heuristic clock alignment plan
   is designed and validated.
