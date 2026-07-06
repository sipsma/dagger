# wcprof x OTel Chunk 1 review - Codex fresh pass

## Verdict

Chunk 1 is close and the core loader shape matches the design: otlpdump JSONL is
deduped, spans compile to the existing wcprof dump IR, `wcprof.parent ?? parentId`
is the only parent rule, wait links compile to native wait events, the replay is
unchanged, and the included simple no-service baseline passes.

I would not treat the §6.1 gate as complete until the wait-edge loss cases below
are made hard failures. The loader itself is sound enough to start Chunk 2 work,
but the gate is not yet strong enough to be the validation backstop Chunk 2 will
need for singleflight wait links.

## Findings

### Medium - The structural gate can pass after wait-edge data has been lost or degraded

The design's safety story depends on wait links being either faithfully compiled
or loudly rejected. Today several incomplete-wait cases degrade into a plausible
graph and still exit successfully.

In [engine/wcprof/wcotel/loader.go](../../engine/wcprof/wcotel/loader.go:309),
non-lock waits resolve the target with `opIDBySpan[normalizeSpanID(l.SpanID)]`.
If the target span is missing or the link target was malformed, `TargetID` stays
zero with no counter. `wcanalyze.Build` then leaves the wait target nil
([engine/wcprof/wcanalyze/graph.go](../../engine/wcprof/wcanalyze/graph.go:255)),
and replay treats it as a fixed delay rather than a join
([engine/wcprof/wcanalyze/replay.go](../../engine/wcprof/wcanalyze/replay.go:168)).
That preserves elapsed time but loses counterfactual propagation to the target
class, which is exactly what the OTel source is supposed to restore.

The same issue exists for malformed wait timing. The loader turns missing or
unparseable `wcprof.wait.*_unix_ns` into a zero-duration wait at the waiter's
start and only increments `MalformedWaitTimings`
([engine/wcprof/wcotel/loader.go](../../engine/wcprof/wcotel/loader.go:313)).
The CLI prints the counter, but `CheckStructural(...).Err()` does not fail on it
([engine/wcprof/wcotel/gate.go](../../engine/wcprof/wcotel/gate.go:102)). For a real
Chunk 2 wait edge, that means a broken timestamp can silently remove the wait
from replay.

Dropped-link detection has a related blind spot. The loader only increments
`WaitBearingDroppedLinks` when a span still has at least one surviving wait link
([engine/wcprof/wcotel/loader.go](../../engine/wcprof/wcotel/loader.go:337)), and
the gate hard-fails only `WaitBearingDroppedLinks` / `WaitLinkDroppedAttrs`
([engine/wcprof/wcotel/gate.go](../../engine/wcprof/wcotel/gate.go:102)). If all
wait links on a span were evicted, or if the `link.purpose` attribute was dropped
so the remaining link is no longer recognizable as a wait, `TotalDroppedLinks`
is only report-only output ([engine/wcprof/wcotel/gate.go](../../engine/wcprof/wcotel/gate.go:138)).

Suggested fix: add explicit counters for unresolved non-lock wait targets and
malformed wait timings and make them gate failures. For dropped links, the local
otlpdump gate should be conservative: either fail on any `TotalDroppedLinks > 0`
in wcprof validation runs, or otherwise fail whenever a span expected to carry
waits reports drops. The current "surviving wait link" predicate is not strong
enough because it can only see what was not evicted.

### Low - The otlpdump front-end discards `traceId`, so multi-trace/appended captures are accepted as one run

`hack/otlpdump` emits `traceId`, but `otlpSpan` and `Span` do not carry it
([engine/wcprof/wcotel/loader.go](../../engine/wcprof/wcotel/loader.go:114),
[engine/wcprof/wcotel/loader.go](../../engine/wcprof/wcotel/loader.go:37)).
The CLI then analyzes each file as one trace
([cmd/wcprof-otel-analyze/main.go](../../cmd/wcprof-otel-analyze/main.go:41)).

That matches the design's intended input, but it is an easy dev-loop footgun
because `hack/otlpdump` appends across runs unless the output file is removed.
An appended JSONL file with two traces will compile into one multi-root graph and
produce a plausible report. I would parse and retain `traceId` in the otlpdump
front-end and reject files with more than one trace id unless an explicit
multi-trace mode is added later.

## Divergence assessment

- (a) `LinkPurposeWait` living in `engine/telemetryattrs` is fine for this repo.
  The key is still `telemetry.LinkPurposeAttr`, and the value is just the new
  vocabulary atom.
- (b) Keeping the shared vocabulary in `engine/telemetryattrs` is justified. It
  is already a zero-dependency leaf used by emit code, and it avoids pulling
  `wcanalyze` or wcprof internals into the engine's telemetry emit path.
- (c) The filled-in mappings are mostly reasonable:
  - non-cached unaugmented success -> `ok` is the right conservative outcome;
    claiming `executed` would be false before Chunk 2 adds `call_exec`.
  - `dag.output` -> dense `ResultID` interner is harmless and keeps the result
    seam available without creating a stringly typed analyzer dependency.
  - in-flight `endNs=0` -> native `OpenOps` is the right target shape for the
    existing `Build` path.
  - malformed wait timing -> zero-duration no-op is acceptable as a parser
    fallback, but it should be a gate failure for wcprof wait links, not merely a
    printed counter. This is covered by the Medium finding.
- The v0.21.7 baseline fixture caveat is acceptable for Chunk 1. It proves the
  unaugmented no-service loader path and gate run, not oracle parity; that is the
  correct strength for this chunk.

I did not find unflagged design drift in op-kind precedence, `wcprof.parent`
encoding, timestamp rebasing, `LinkCountLimit`, or the otlpdump dropped-count
extension.

## Correctness notes

- Live span dedup keeps the ended copy and is covered by tests.
- Self-parenting is handled by clearing `ParentID` when the causal parent resolves
  to the same op.
- Empty input returns an error rather than producing an empty graph.
- Missing wait targets are not currently counted or failed; see the Medium
  finding.
- The loader maps every completed span to one op and does not synthesize causal
  nodes, timestamp containment parents, or wait edges from non-wait links. That
  preserves the zero-inference contract.

## Performance and simplicity

No degenerate performance issue found in Chunk 1. Compile is `O(spans log spans +
links)` from the deterministic sort plus linear maps; wait-link translation is
linear in link count. The structural gate invokes one baseline replay and then
uses the existing cached self-time path. The code is direct and scoped; I did not
see excessive abstraction.

## Validation performed

- `go test ./engine/wcprof/wcotel`
- `go test ./cmd/wcprof-otel-analyze`
- `go test ./hack/otlpdump`
- `go test ./engine/server`
- `go test ./engine/wcprof/...`
- `go run ./cmd/wcprof-otel-analyze ./engine/wcprof/wcotel/testdata/baseline-simple-noservice.jsonl`
- `git diff --check upstream/main...HEAD`

The CLI baseline run passed the structural gate with `ops=60`, `roots=1`,
`wait-edges=0`, `cycles=0`, `fallback-anchors=0`, and no dropped links.
