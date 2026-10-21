# Spike 9: engine-backed immutable tool results

This is a research spike, enabled only by `_DAGGER_SPIKE9=1`. It exposes a generic
map `(provider version, toolchain, key) -> File`, exercised by a Go `GOCACHEPROG`
client. Go-specific code is confined to the harness. See [linux-vm-spec.md](linux-vm-spec.md)
for the measurement plan.

## Engine semantics

`_spike9Get` probes an ordinary DagQL cached call. A miss returns no File and is
not cached. `_spike9Put` evaluates its File, hashes its content and metadata, then
publishes an independently owned immutable snapshot under that same call key.
The result has a content digest and a normal persistable cache root. It retains
no filesync source recipe or client-local path dependency. Provider, toolchain
and key are all part of lookup identity; a run-specific provider namespace makes
the benchmark cold without deleting engine state.

Publication is serialized within one engine using fixed striped locks. Equal
publications converge; conflicting content for an existing key fails without
replacing it. Imports and hashing happen before the lock. Gets racing an
unfinished put can return a miss; a get after successful publication sees it.
Locks hold no entries. Normal Dagger snapshot ownership, persistence and GC
apply. The focused core test checks publication races, misses, key separation
and persistence across a cache restart.

The provider owns the correctness of its keys. This is not a sandbox for
untrusted providers: authentication, tenant separation, namespace allocation,
quotas, and cross-engine conflicting publication policy are unimplemented. The
per-engine mutex cannot arbitrate two remote publishers. Files with services
are rejected. Only a File is supported, with file name, platform and metadata
in its content identity. GC may evict an entry, yielding another miss; this is a
cache, not durable application storage. A production API would need to define
these semantics and handle transfer conflicts before exposing it broadly.

## Live materialization and actual traffic

Every Go get performs one engine query selecting File ID, name and size. A hit
then uses `File.export` through the current session's filesync attachable to
produce a local path while Go is still running. The helper coalesces repeated
exports of the same output within one Go invocation. It still performs every
key lookup; it does not memoize get results or misses. A new local directory is
used for each build and deleted afterwards. No live mounts are implemented.

For a put, the helper decodes and checks Go's SHA-256 output ID, writes a staging
file and hardlinks it into the invocation's local directory. It calls
`Host.file(...).id`, then `_spike9Put`. The second call includes the lazy import,
content hashing, immutable publication and File metadata selection. The helper
returns the local path only after publication succeeds. Empty files and repeated
outputs use the same path. No cache volume or local object/action map backs the
store. Staging and materialized paths exist only to satisfy Go's protocol.

The final binary is retained in the harness's output Directory as a Dagger File.
Its export to the runner is outside build timing. Dependencies are downloaded
before the series; the target standard library is not compiled in preparation.
The helper is compiled in a separate container. Each arm/round starts with a
fresh GOCACHE, and the engine arm has a fresh versioned namespace. First, edit,
then no-change builds share that round's engine entries. Target compile caches
are not shared between arms. The same source path and build flags are used.

## What the counters mean

`results.jsonl` and per-build `analysis.json` retain every request kind's count,
hits/misses, errors, logical bytes and helper intervals. `GetNS` includes the
lookup and File metadata query. `ImportNS` measures the initial lazy Host.file
ID request, **not** the full import; `PutNS` includes the subsequent import,
hash and publication. Their sum is the client-observed put RPC cost. Separate
get-hit/get-miss p50/p95 values and put p50/p95 values are recorded.

`MaterializeNS` includes actual exports or waiting for/reusing an invocation's
existing path; `ExportP50NS/P95NS` select exports with nonempty payloads. Byte
counts count each unique exported payload once within an invocation. Put body
bytes count all protocol bodies, even duplicate content. `EngineRequests` and
engine request/response byte counts cover GraphQL JSON; they exclude bulk
filesync wire encoding. Logical body/export bytes are provided instead.
`StoreReadBytes` is a retained spike-7 field and is zero here; it does not measure
snapshot reads. `StoreWrittenBytes` describes helper staging writes only.

`StoreNS` contains engine work and materialization; it overlaps those intervals.
`HelperNS` contains body receive/decode, queueing, RPCs and response serialization
but not caller-side encoding/hashing or waiting for the next request header.
Concurrent intervals overlap and must never be summed into a wall-time budget.
Build wall time is measured around `go build` only. Container setup, dependency
downloads, fixture checks, binary export, offline analysis and source edits are
excluded. Functional mode suppresses build elapsed values and must not be used
for timing conclusions. Timing runs use no wcprof instrumentation.

## Transfer evidence and the remote-cache gate

Two fresh dev engines have separate state directories and fixture roots. The
producer publishes a File under two keys, verifies a pre-publication miss became
a hit, and exports it with the **existing** `_remoteCacheFixture` transfer path.
The consumer initially misses, imports the result bundle and snapshot blobs,
then looks up the original keys and exports/checks the bytes. It makes no puts.
Fixture reports must show provider reads and an installed snapshot chain.
Only the bundle and blobs cross between engines; no producer state or original
client files do. The fixture transport uses disposable harness volumes, not a
store cache volume. Engine state volumes are ordinary isolated dev-engine state.
Normal GC remains enabled.

This demonstrates a content-addressed Dagger result and transferable snapshot,
including lookup identity on a second engine. **It does not demonstrate a real
remote service round trip. Real remote lookups, pulls, authentication, network
latency, eviction, remote query indexing and concurrent remote publication are
unmeasured.** No remote endpoint or credentials are used. Under option (b), the
representation/fixture check is evidence of compatibility; the end-to-end real
remote-cache pass/fail gate remains unverified.

## Package visibility

Engine entries have opaque provider/toolchain/key metadata, not package records,
dependency edges or compiler spans. The harness emits Go's `-debug-actiongraph`
in both arms and separately correlates request IDs with its 120-bit ActionID
prefixes and a limited source-known list of Go subkeys. These are diagnostic
matches, never cache keys. Per-build analysis reports matched, ambiguous and
unmapped fractions. Graph edges are available offline; action timestamps include
cache I/O, and aggregate command duration does not supply exact compiler
start/end spans. A production package view needs a tool integration or additional
command instrumentation. Offline correlation time is reported separately;
action-graph emission remains inside both build timings and its incremental
cost is not isolated by this spike.
