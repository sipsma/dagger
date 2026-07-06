# wcprof x OTel Chunk 1 Code Review

Reviewed implementation branch `wcprof-otel-implementer-7a7ee34b`, commit
`e689e9b007`, against `upstream/main` `b442cd2533`.

## Overall Verdict

Chunk 1 is close, but I would not build Chunk 2 on it until the wait-link
validation holes below are closed. The loader's happy-path mapping is mostly
faithful to design section 5, the baseline DoD test exists and passes, and I did not
find a degenerate performance shape. The blocking problem is that malformed or
partial wait-link data can still produce a gate-passing graph with wrong replay
semantics.

## REAL Issues

### HIGH - Missing non-lock wait targets silently become fixed waits

The design's wait-link mapping is explicit: a `purpose=wait` link becomes a wait
event whose `TargetID` is the link target span id resolved to an op, except for
`lock` waits which use an ident instead (`hack/designs/wcprof-otel-design.md:855`
to `hack/designs/wcprof-otel-design.md:860`). Invariant T is built around the
same requirement: the emitter must make the target span context available before
the waiter can publish the wait (`hack/designs/wcprof-otel-design.md:363` to
`hack/designs/wcprof-otel-design.md:389`).

The implementation does not fail when that resolution fails. In
`engine/wcprof/wcotel/loader.go:306` to `engine/wcprof/wcotel/loader.go:311`, a
non-lock wait does `targetID = opIDBySpan[normalizeSpanID(l.SpanID)]`; if the
linked span is absent, malformed, outside the trace, or lost by a front-end bug,
the map returns zero. The loader still emits the wait event at
`engine/wcprof/wcotel/loader.go:327` to `engine/wcprof/wcotel/loader.go:335`.

That zero target is not harmless. `wcanalyze.Build` only attaches a wait target
if `g.Ops[rw.targetID]` exists, otherwise the target remains nil except for the
native-style `reason=="exec"` ident fallback (`engine/wcprof/wcanalyze/graph.go:247`
to `engine/wcprof/wcanalyze/graph.go:261`). Replay then classifies nil-target
waits as `actWaitFixed` (`engine/wcprof/wcanalyze/replay.go:162` to
`engine/wcprof/wcanalyze/replay.go:170`) and adds their duration as fixed time
(`engine/wcprof/wcanalyze/replay.go:383` to
`engine/wcprof/wcanalyze/replay.go:384`). Self-time also subtracts every wait
interval regardless of whether it resolved to a target (`engine/wcprof/wcanalyze/graph.go:379`
to `engine/wcprof/wcanalyze/graph.go:391`).

Impact: an Invariant T regression, Cloud/front-end truncation, or typo in a wait
link becomes a plausible but wrong graph. The time is no longer attributed to the
target class, scaling the real target cannot shorten the wait, and the structural
gate passes because there is no missing-target counter or violation. This is
exactly the kind of emit/front-end failure Chunk 1's gate is supposed to make
loud before Chunk 2 starts emitting singleflight waits.

Recommended fix: track missing non-lock wait targets during compile and hard-fail
the gate when the count is nonzero. `lock` is the intentional targetless case. If
OTel ever intentionally wants the native `exec` ident fallback, that should be an
explicit design/loader rule; the current design says the wait link carries the
target span id.

### MEDIUM - Malformed wait timestamps are report-only, so bad waits can pass the gate

The design requires wait intervals to be parsed from exact decimal-string Unix
nanos and rebased to the same epoch as span intervals
(`hack/designs/wcprof-otel-design.md:855` to
`hack/designs/wcprof-otel-design.md:862`). The Cloud round-trip test is also
explicitly supposed to assert every wait link's `wcprof.wait.*_unix_ns` strings
survived intact, parseable, and bit-exact
(`hack/designs/wcprof-otel-design.md:1012` to
`hack/designs/wcprof-otel-design.md:1019`).

The loader recognizes malformed wait timings, but then substitutes a
zero-duration interval at the waiter's start and still emits the wait event
(`engine/wcprof/wcotel/loader.go:313` to
`engine/wcprof/wcotel/loader.go:335`). The structural gate records
`MalformedWaitTimings` (`engine/wcprof/wcotel/gate.go:49` to
`engine/wcprof/wcotel/gate.go:53`, `engine/wcprof/wcotel/gate.go:60` to
`engine/wcprof/wcotel/gate.go:73`) and prints it (`engine/wcprof/wcotel/gate.go:137`
to `engine/wcprof/wcotel/gate.go:139`), but it never turns that counter into a
violation (`engine/wcprof/wcotel/gate.go:93` to
`engine/wcprof/wcotel/gate.go:110`).

Impact: a bad emitter, lost link attributes, or Cloud/front-end decode bug can
make real waits disappear from replay while the command still says
`structural gate: PASS`. The counter is useful diagnostics, but for augmented
traces a malformed wait interval is not a tolerable partial success. It can make
Chunk 2 fixtures and cap-stress runs look green while under-serializing the
graph.

Recommended fix: keep the robust zero-duration substitution if it helps
rendering, but make `MalformedWaitTimings > 0` a hard structural-gate failure.
Add tests for missing start, missing end, and unparseable decimal strings.

## NOISE / Verified Fine

- `link.purpose="wait"` value in `engine/telemetryattrs` is acceptable for this
  repo. The key remains `telemetry.LinkPurposeAttr` from `github.com/dagger/otel-go`
  (`engine/wcprof/wcotel/loader.go:299`), and the local value is documented next
  to the rest of the inert engine vocabulary (`engine/telemetryattrs/attrs.go:90`
  to `engine/telemetryattrs/attrs.go:94`). This is not a correctness issue.
- Keeping the wcprof vocabulary constants in `engine/telemetryattrs` is a
  pragmatic placement. The package already exists as a low-level engine
  attributes package, and this avoids pulling analyzer code into the engine
  binary (`engine/telemetryattrs/attrs.go:38` to
  `engine/telemetryattrs/attrs.go:47`).
- The un-augmented success outcome mapping to `"ok"` is the right conservative
  choice for Chunk 1. The current OTel span does not distinguish executed versus
  joined without the future `call_exec` shape, so `computeOutcome` avoids
  over-claiming (`engine/wcprof/wcotel/loader.go:380` to
  `engine/wcprof/wcotel/loader.go:395`).
- Interning `dag.output` strings into wcprof result IDs is fine. The current
  emitter writes `DagOutputAttr` as an OTel string (`core/telemetry.go:286` to
  `core/telemetry.go:288`), and the IDs only need to be stable within one
  compiled trace (`engine/wcprof/wcotel/loader.go:259` to
  `engine/wcprof/wcotel/loader.go:262`).
- In-flight spans as `OpenOps` match `Build`'s native contract. The loader puts
  `EndUnixNS==0` spans into `Header.OpenOps` (`engine/wcprof/wcotel/loader.go:264`
  to `engine/wcprof/wcotel/loader.go:277`), and `Build` ends open ops at dump
  time (`engine/wcprof/wcanalyze/graph.go:216` to
  `engine/wcprof/wcanalyze/graph.go:234`).
- The live-export dedup rule is correct for the otlpdump path. It keys by span id
  and keeps the copy with the largest end timestamp (`engine/wcprof/wcotel/loader.go:179`
  to `engine/wcprof/wcotel/loader.go:188`), and there is focused coverage in
  `TestParseDedupKeepsEndedCopy` (`engine/wcprof/wcotel/loader_test.go:78` to
  `engine/wcprof/wcotel/loader_test.go:99`).
- `LinkCountLimit = 16384` is applied using `sdktrace.NewSpanLimits()` before
  overriding only the link limit (`engine/server/session.go:684` to
  `engine/server/session.go:695`). That preserves the other SDK defaults and
  matches the implementation-plan warning about `WithRawSpanLimits`.
- `hack/otlpdump` now exposes both span-level dropped links and per-link dropped
  attrs (`hack/otlpdump/main.go:119` to `hack/otlpdump/main.go:139`), which is
  the necessary local signal for the gate.
- I do not see accidental quadratic behavior in the loader. Compile is
  `O(spans log spans + links)` for dedup/sort/mapping, and the gate delegates
  the expensive semantics to the existing replay. The added tests are small and
  targeted.
- The v0.21.7 baseline fixture caveat is acceptable for Chunk 1. It exercises the
  un-augmented otlpdump shape, live-span dedup, loader report rendering, and the
  no-service structural gate (`engine/wcprof/wcotel/baseline_test.go:13` to
  `engine/wcprof/wcotel/baseline_test.go:79`). Refreshing it from engine-dev
  before final integration would still be cleaner.

## Suggestions

- Consider carrying `traceId` through the otlpdump front-end and rejecting files
  with more than one trace id. The design input is one trace
  (`hack/designs/wcprof-otel-design.md:820` to
  `hack/designs/wcprof-otel-design.md:824`) and multi-trace aggregation is
  explicitly out of scope (`hack/designs/wcprof-otel-design.md:1219` to
  `hack/designs/wcprof-otel-design.md:1224`). `hack/otlpdump` emits `traceId`
  (`hack/otlpdump/main.go:110` to `hack/otlpdump/main.go:113`), but the loader's
  `otlpSpan` drops it (`engine/wcprof/wcotel/loader.go:114` to
  `engine/wcprof/wcotel/loader.go:125`). This is not a Chunk 2 blocker if every
  fixture and Cloud query is already one trace, but rejecting accidental local
  aggregation would make the tool safer.
- The `interval > trace span` gate is harmless but weak: `TraceSpanNS` is computed
  from the same graph's min start and max end (`engine/wcprof/wcotel/gate.go:66`
  to `engine/wcprof/wcotel/gate.go:68`, `engine/wcprof/wcanalyze/graph.go:320`
  to `engine/wcprof/wcanalyze/graph.go:329`), so an individual op duration should
  rarely, if ever, exceed it by construction. The useful service/leak signal is
  more likely the existing self-time-vs-makespan check.

## Tests Run

Ran against detached worktree at commit `e689e9b007`:

```sh
go test ./engine/wcprof/wcotel ./cmd/wcprof-otel-analyze ./hack/otlpdump
```

Result: pass.
