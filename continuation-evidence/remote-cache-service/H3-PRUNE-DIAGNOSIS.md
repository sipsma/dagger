# H3 diagnosis: engine B prunes the imported bundle before its client reads

Engine seat, 17 September 2026. Engine branch `engine-seat-0bf270c0`, candidate
`b15c2873f5`. Plan: `hack/designs/remote-cache/service-plan.md` (section 9.3,
the first test). The failure and the evidence below are from the H3 runs of
that day; the diagnostic code that produced the lookup records was never
committed.

## The failure

`TestColdEngineReusesResult` and `TestColdEngineDirectoryFunction` in
dagger.io `remote-cache/e2e`: engine A exports the `build` leaf, engine B
imports every bundle with `ok` before its client connects, then B runs
`BODY-RAN Build` and the exec again. B's file value and token differ from
A's. Zero downloads. Seen in the service seat's runs at 19:23 and 19:41 and
reproduced in the engine seat's runs at 19:57 and 20:11, all with both tests
running in parallel. A run of `TestColdEngineReusesResult` alone at 19:51
passed on the same engine: equal tokens, equal file values, body not run.

## The evidence chain

1. Recipe digests differ between the engines for the constructor and the
   function call, as expected, because a function call's recipe hashes the
   scoped module's recipe (`dagql/result_call_frame.go:760`), which includes
   the per-client `asModule` chain. 19:23 run: `Query.cacheDemo` A
   `xxh3:8bdec63b5a3d1523`, B `xxh3:71e4c957e7151b35`; `CacheDemo.build` A
   `xxh3:601194b45144ed18`, B `xxh3:957b940489e77027`. This is not the
   cause: in the passing 19:51 run the digests differ the same way
   (`cacheDemo` A `xxh3:89c668a501d5033f`, B `xxh3:12d124b3685a04fa`) and B
   hits both calls, `cached=true`, through the structural lookup.
2. The structural bridge works. The scoped module result carries its content
   digest as a remote-cache-labeled extra (`core/schema/module.go:3078`,
   kept by `transferExtras`, `dagql/cache_value_codec.go:12`), so A's
   imported row and B's own row share one eq-class. B's lookup record for
   the miss in the 20:11 run: `MISS type=CacheDemo field=cacheDemo
   inputs=[<B's module recipe>(class=4314 members=[xxh3:03e9dd7a7460c612
   xxh3:21dd98b971d9480b xxh3:9eacf8d615e33fa7 xxh3:a0a206c44eaa09dd])]
   termSet=0 candidatesIncompatible=0 sawExpired=false`. The class holds the
   content digest and both engines' recipes. No session-resource refusal:
   host directories carry no session resource handle (only unix and SSH
   sockets and secrets do, `core/schema/host.go:443`, `:535`).
3. The imported rows were gone. The same record lists every row of the
   requested type and field present on B at that moment: none of type
   `CacheDemo` field `cacheDemo`, none for `build`, and for
   `Module._implementationScoped` only B's own row 4343. Lower rows of the
   same bundle were still there: `moduleSource` 4234 and `asModule` 4316,
   both imported.
4. B's engine log names what removed them: `dagql pruned result
   resultID=4318 call=Query.cacheDemo` and `dagql pruned result
   resultID=4325 call=CacheDemo.build`, with 4318 and 4325 inside B's
   imported-row range (4229 to about 4330), plus pruned
   `Query._builtinContainer`, `Query.__schemaJSONFile`,
   `Query._clientFilesyncMirror` and dozens of `TypeDef` rows. That message
   is logged by the disk prune stage, `dagql/cache_prune.go:335`.
5. In the passing single run the same prune ran and logged `dagql prune skip
   policy: no reclaim target` for all three policies.

## The mechanism

The nested engines run with no GC configuration. `engine/server/gc.go` then
uses the default disk policies, whose thresholds are percentages of the disk
behind the engine's root directory (`disk.GetDiskStat(srv.rootDir)`,
`engine/server/gc.go:342`), which in this harness is the host's root disk.
The pressure monitor checks every 5 seconds (`localCachePressureCheckEvery`)
and runs GC when the disk's available space is below a policy's
`MinFreeSpace` (`engine/server/gc.go:457`); the GC scheduled one second after
every session removal does the same. The disk stage prunes the least
recently used retained roots first, whatever their disk size: the pruned
rows log `measuredSizeBytes=0`. An imported bundle root is a retained
result with no session use and a last-use time equal to its import time, so
it is the first LRU victim. Pruning it collects `cacheDemo`, `build` and the
scoped module; rows that another root still holds survive, which is why the
lower rows were still present.

The host's root disk was 100% full earlier that day and stood at 78% to 81%
(94 to 106 GB free of 493 GB) during these runs. Four nested engines and
their builds in a parallel run push the free space under the default
threshold; one test alone did not.

## Two fixes

1. Harness, demo-grade, immediate: give both nested engines an
   `engine.json` whose GC bounds never reclaim, as the predecessor's
   two-engine test does "independently of host disk pressure":
   `core/integration/remote_cache_transfer_test.go:160` applies
   `engineConfigWithGC("1000000000000000", "0", "1000000000000000", "0")`
   (`core/integration/localcache_test.go:1502`) through `engineWithConfig`
   (`core/integration/engine_test.go:101`). Routed to the service seat by
   the coordinator. Keeping the host's free space well above the default
   threshold matters too.
2. Engine, a pruning behavior change the plan's section 15 defers and the
   coordinator has raised with Erik: treat a freshly imported root like a
   freshly computed one for LRU order (touch its last-use at import), or
   exempt imported roots from the disk stage for a grace period. Without
   it, a cold engine under disk pressure loses its imports before its first
   client connects. Not coded.

## How the lookup records were obtained

The nested engines' stdout is dropped by the outer CLI's progress under the
parallel load (B's client progress shows `CacheDemo.build DONE (27.8%
dropped)`; the parallel runs contain zero `end call` lines against 121 in
the single run), so an uncommitted diagnostic recorded, per session, one
line per lookup decision on `cacheDemo`, `build`, `buildDirectory` and
`_implementationScoped`, and `Cache.SessionResults` appended those lines to
the session report as pseudo-entries of type `DIAG`, which the service
ignores and the test saves as an artifact. The pruned-result lines are
ordinary engine log lines that happened to survive the dropping.
