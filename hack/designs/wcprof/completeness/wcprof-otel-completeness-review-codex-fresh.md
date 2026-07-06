# wcprof OTel Completeness Checksum Review - Codex Fresh

Verdict: **not landable yet**. The change catches the direct "declared N, received N-1" leaf-drop case, and the shared loader/gate plumbing is mechanically clean. But the checksum is still only a cardinality comparison against the largest marker that survived. That leaves real false-pass paths for silent incompleteness, including exactly the class this safety mechanism is meant to close.

## Findings

### BLOCKER: post-marker surplus spans can mask a dropped counted leaf

`wcprofSpanCounter.OnStart` marks and increments **every** engine span created by a registered provider (`engine/server/wcprofcount.go:54-63`). `Stamp` writes the running count onto the current `POST /query` span at query return (`engine/server/wcprofcount.go:78-93`, registered in `engine/server/session.go:1466-1482`). The producer comments explicitly acknowledge engine spans can be created after the last query stamp (`engine/server/wcprofcount.go:38-44`).

The loader then counts every received marked span with no cutoff or epoch (`engine/wcprof/wcotel/loader.go:268-271`) and only reports missing spans when `declared > received` (`engine/wcprof/wcotel/loader.go:279-280`); the gate only hard-fails marker-absent or `MissingSpans > 0` (`engine/wcprof/wcotel/gate.go:164-168`).

That means a dropped leaf inside the declared population can be exactly offset by any later marked engine span that was not included in the marker:

```text
declared at final query stamp = 5
one declared leaf drops       = -1
one post-stamp span arrives   = +1
received marked spans         = 5
MissingSpans                  = 0
gate                          = PASS
```

I verified this against the exported `wcotel.Compile` + `CheckStructural` API with a small out-of-tree harness: a trace with a missing pre-stamp leaf and one post-stamp extra marked span reports `marker=true declared=5 received=5 missing=0 err=<nil>`.

This is not just theoretical because the implementation deliberately permits post-stamp engine spans. Reaping also currently happens before session cleanup (`engine/server/session.go:425-428`), while service stop / telemetry shutdown work follows (`engine/server/session.go:438`, `engine/server/session.go:477-478`); any traced spans started there would also be marked but not declared, and can create the same surplus.

The checksum needs a closed population. Either the producer must stop marking/counting spans outside the declared population, or the declaration must be emitted after all countable spans, and the gate should not allow surplus to compensate for loss. As written, `received >= declared` is not a proof of completeness.

### BLOCKER: with multiple query markers, dropping the final marker and a closed tail can pass

The multi-query fix stamps a running total on every main-client `POST /query` and has the loader keep the maximum marker it receives (`engine/server/wcprofcount.go:30-35`, `engine/server/session.go:1475-1482`, `engine/wcprof/wcotel/loader.go:272-276`). This fixes fragmentation only if the final/highest marker survives.

If the final marker span is dropped along with a later closed subtree, an earlier lower marker remains present and reconciles cleanly. Because the missing subtree is closed, it need not create an orphaned parent or unresolved wait target. I verified that shape against the loader/gate too: a trace containing only the first query marker `declared=3` and its three spans passes with `marker=true declared=3 received=3 missing=0 err=<nil>`, even though the actual trace could have had a later marker `declared=5` whose whole subtree was lost.

So "root marker drop -> absent -> refuse" only holds for a single-marker trace. With a real `dagger -c` multi-query trace, a dropped final marker can downgrade the declared total to an older prefix. The current max-of-received-markers rule does not prove finality.

### MEDIUM: cleanup ordering can reintroduce counter entries after `Reap`

`removeDaggerSession` calls `Reap` before stopping services, releasing containers, and shutting down telemetry (`engine/server/session.go:425-428`, then `engine/server/session.go:438-478`). If any of that work starts spans under the same trace, `OnStart` will recreate `counts[tid]` after it was deleted (`engine/server/wcprofcount.go:61-63`) and no later `Reap` will remove it.

This is secondary to the correctness issue above, but it is another sign the producer lifecycle has not defined a closed "all countable spans are now known" point.

## What Looks Sound

- The producer side is cheap and correctly per-trace keyed for concurrent traces: one shared processor is installed before live export on every per-client provider (`engine/server/session.go:717-724`), and the counter map is keyed by `trace.TraceID` (`engine/server/wcprofcount.go:45-63`).
- Live start/end duplicates are dedup-safe on the loader side because `Compile` keeps one span per span ID before counting (`engine/wcprof/wcotel/loader.go:234-256`).
- Marker-absent fail-by-default is correctly implemented (`engine/wcprof/wcotel/gate.go:164-165`) and tested (`engine/wcprof/wcotel/completeness_test.go:67-87`).
- The direct dropped-leaf test is the right minimal proof for the easy case (`engine/wcprof/wcotel/completeness_test.go:17-65`), but it does not cover surplus masking or final-marker loss.
- Both front-ends share the same `wcotel.Compile` and gate; `wccloud` remains a pure field map into `wcotel.Span` (`engine/wcprof/wccloud/cloud.go:36-62`, `engine/wcprof/wccloud/cloud.go:104-117`).
- The round-trip tightening to structural graph fingerprints is a real improvement over count-only equality (`engine/wcprof/wccloud/roundtrip_cloud_test.go:153-158`).
- The CLI change separating report-write errors from gate failures is correct (`cmd/wcprof-otel-analyze/main.go:97-107`, `cmd/wcprof-otel-analyze/main.go:119-146`).
- No `wcanalyze` replay behavior changed. The loader gained provenance/count checks; the causal compilation/replay model remains intact.

## Test Notes

Focused tests passed:

```text
go test ./engine/wcprof/wcotel
go test ./engine/wcprof/wccloud
go test ./cmd/wcprof-otel-analyze
go test ./engine/server -run '^$'
```

Test gap: the fixture helpers stamp `wcprof.engine_span=true` onto every synthetic span and put the marker on the first/root fixture span (`engine/wcprof/wcotel/loader_test.go:48-70`, `engine/wcprof/wccloud/cloud_test.go:154-180`). That is fine for exercising loader arithmetic, but it does not validate the real engine-only population boundary or the post-stamp surplus cases above.

## Checklist Answer

- Count is distinct and per-trace in the ordinary case, but the declared population is not closed.
- Loader/gate are shared across local and Cloud and fail closed when no marker is present.
- The simple leaf-drop hole is closed; residual silent incompleteness remains via surplus masking and final-marker loss.
- `received > declared` is not harmless under this producer shape; it can hide a missing declared span.
- The current caveat is not acceptable for a merge gate safety mechanism.
