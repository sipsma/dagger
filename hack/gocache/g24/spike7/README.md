# Spike 7: Go cache protocol bridge

This is a local model of an engine-owned action store, not that store's
implementation. It measures the real GOCACHEPROG protocol and Go's scheduler
with controlled helper delay. It does not modify the engine or the official Go
module. All Go commands run in a golang container through Dagger.

Risk covered: a cache-protocol bridge may erase Go's scheduling advantage through
serialized archive traffic and materialization; this affects dagger.io/go users
and other modules considering tool-owned schedulers.

## Run

Base: dagger/dagger 49065f5de3. Requires Bash, git, timeout and
`~/.local/bin/dagger` v1.0.0-beta.19. No helper installation, host Go, Python,
dev engine or cache volume is needed. Dagger pulls golang:1.26.6 pinned by its
multi-platform manifest digest in run.sh; both arms use
that identical toolchain with CGO disabled and GOTOOLCHAIN=local. This is a new
matched comparison, not a reproduction of item 4's Go 1.24 yq times.

From the repository root, on linux-vm only:

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
git fetch https://github.com/sipsma/dagger.git gocache-g24-spike7-cacheprog
git switch --detach FETCH_HEAD
export YQ="$HOME/gocache-bench/yq"
export DSRC="$PWD"
bash hack/gocache/g24/spike7/linux-vm.sh "$HOME/g24-spike7-$(date -u +%Y%m%dT%H%M%SZ)"
```

Use a clean checkout for source (tracked changes are rejected). The yq source
must be v4.49.2, commit 7ccaf8e700ce99eb3f0f6cef7f5930a0b3c827cd.
DSRC may point to the prior scale workload checkout instead; record its commit.
The runner checks load1 <= 16 before each Dagger invocation. Reschedule a busy
slot; do not disable GC or stop another engine.

The slot is budgeted for 20–30 minutes, not calibrated by a local timing series.
The script caps yq, including preparation, at 16 minutes; the combined cap is
28 minutes. It runs dagger only if eight minutes remain. A deadline/failure is
not a data point: retain partial artifacts and mark the slot incomplete.

The yq series has four rounds; each arm executes first build, edit, then no-change:

| Round | Arm order |
|---|---|
| 1 | plain, helper0, helper3, helper1 |
| 2 | helper0, helper1, plain, helper3 |
| 3 | helper1, helper3, helper0, plain |
| 4 | helper3, plain, helper1, helper0 |

These balanced orders rotate position and first-order carryover. Dagger uses
the first two rounds (a smaller exploratory sample, not four-round balance).
There are 48 yq builds and, if included, 24 dagger builds. The suffix denotes
0, 1 or 3 ms of injected delay per helper request. Build parallelism and
GOMAXPROCS are 12; helper concurrency is 64. All runs are sequential.

A single-project invocation, also useful for a failed slot, is:

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
SPIKE7_PARALLEL=12 bash hack/gocache/g24/spike7/run.sh "$HOME/g24-spike7-yq-one" yq timing 1
```

Functional check (not a timing series):

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
YQ=/path/to/yq-v4.49.2 bash hack/gocache/g24/spike7/run.sh "$HOME/g24-spike7-functional" yq functional 1
```

Functional mode runs one plain/helper1 sequence, suppresses build wall times,
checks hits/puts/materialization and request correlation, and starts yq with
`--version`. It does not execute the compiled dagger CLI. On the shared
development host, set SPIKE7_CLOUD_WRAPPER to the approved cloud.sh if load is
high; the script forces cloud even through the wrapper's fallback. This optional
host policy wrapper is not required on linux-vm.

## What is held constant

Preparation downloads modules and compiles/tests this helper in separate Dagger
steps. It never builds the target. The target's standard library is cold:
each arm gets a new ordinary GOCACHE and, for helper arms, a new backing store.
Only the helper binary is copied from its build container; its compiler cache
does not enter the experiment.

Each arm uses the same workspace path. Source copying and edits are outside the
timed build brackets. Every build uses `-mod=readonly`, offline module lookup,
the same `-p` and `-debug-actiongraph`. The binary has a fresh path per arm,
which stays fixed across its first/edit/no-change sequence. The graph's instrumentation
cost is paid by all arms; this is not an uninstrumented go-build comparison.
The binary, ordinary cache and helper store persist across that arm's three
builds. The helper's local materialization directory is new for each Go process.

The yq edit is the literal equivalent of g23 inner-realpp.sh's edit A:
`unable to parse duration [%v]: %w` becomes
`unable to parse the duration [%v]: %w` in pkg/yqlib/operator_add.go.
Dagger builds ./cmd/dagger and applies edit D in internal/cloud/auth/auth.go:
`could not acquire lock on %s: timed out` becomes
`could not acquire the lock on %s: timed out`. Exactly one match is required.
No-change is a regression guard, not the optimization target.

A nonce prevents Dagger from returning an earlier whole-experiment result.
The measurements bracket only the go-build subprocess, including helper startup
and shutdown. Dagger setup, helper tests, downloads, source copies, analysis and
binary smoke checks are excluded and must be reported separately.

## Helper model

`/spike7 helper -store ... -local ... -log ... -latency 1ms -workers 64 -materialization copy`

The protocol reader streams base64 puts to a staged file while computing and
checking their SHA-256 output ID. It does not buffer an archive in memory.
Workers independently delay and serve requests; responses may be out of order.
The delay is one simulated round trip per get/put/close, not a global serial
sleep. The real Go-side serialized JSON/base64 writer remains in the path.

Complete blobs are published by hard link. Action metadata is published by
atomic rename, under a lock that rejects conflicting values for the same key.
Misses are not retained. This supports idempotent publication and miss-then-put
within the model. It is neither crash-durable (no fsync) nor a multi-process
production cache service; each experiment has one helper process at a time.

In default copy mode, the helper copies a returned blob to an invocation-local
file once per output ID, including puts. Later requests in the same process
reuse that file. Direct mode (`SPIKE7_MATERIALIZATION=direct`) returns the
backing file path and provides an optional lower-bound experiment; it is not
part of the default series. All store and materialized files are private to
one Dagger exec sequence. No cache volumes are used.

The model leaves out engine graph/cache lookup, RPC serialization beyond Go's
own protocol, network bandwidth/transfer, remote misses, snapshot mounting,
GC, remote export, multi-tenant authorization and contention, and package-record
publication. Injected delay does not model bandwidth, CPU consumption, tail
latency or those features' correlations. Neither plain nor helper storage is
crash-durable here. Local page-cache effects remain and are balanced only by
run order. A favorable result is evidence to build a real bridge, not a claim
that a Dagger bridge will attain the same time.

## Artifacts and interpretation

Each project exports run/PASS, exitcode, meta.json, results.jsonl, summary.txt,
prepared/download.log and the relevant source files of its actual Go toolchain.
Each build exports:

- graph.json: unmodified Go action graph;
- build.log: compiler/helper errors;
- requests.jsonl: one event per get, put and close;
- requests.jsonl.wire.json: total stdin/stdout bytes, including protocol
  whitespace and the capability handshake;
- analysis.json: totals and per-request helper duration percentiles by kind;
- request-packages.json: classification of every get/put;
- package-records.json: graph nodes, package names, dependency IDs, action
  timestamps and command duration fields.

Per-request request_bytes counts parsed headers and body framing; empty
inter-record whitespace is included in the raw wire totals instead.
body_bytes counts decoded put content. materialized_bytes counts actual copies,
once per output per process. store_written_bytes includes staged bodies and
new metadata; store_read_bytes includes metadata reads and materialization.
These are logical filesystem bytes, not physical I/O.

helper_ns starts once the complete request header has been read and ends after
the response write. It includes body decoding/storage, worker-queue wait,
injected delay, lookup/publication, materialization and response-lock/write time.
The per-request log write and idle time awaiting a header are outside that
bracket. Go-side encoding/hashing and response consumption are also outside it,
but inside the overall build timer. Durations overlap: never sum them into
elapsed build time. Close waits for workers; its helper_ns includes that wait.

Report each round's first/edit/no-change wall time and ratios against that
round's plain arm; show all samples and medians, not just the best.
For each phase and latency report gets/hits/misses, puts, decoded bytes,
raw wire bytes, materialized bytes, helper p50/p95/max, and command-bearing graph
actions. Separate graph/store lookups from real compiler actions.
Report failures and excluded preparation time. Preserve all raw files.

## Package visibility

The protocol itself has no package names, dependency edges or execution events.
Offline correlation uses the graph's ActionID, which is a 120-bit base64url
prefix of the helper's full 256-bit action key. Unique prefix matches are marked
direct-prefix; any ambiguity is reported, never silently chosen. These prefixes
are diagnostic only, never cache keys.

A second pass recognizes a fixed list of Go's known subkeys (stdout, srcfiles,
and several cgo/vet keys) from full direct keys seen in the request log. Report
both the direct fraction and the direct-plus-known-subkey fraction of all
get/put requests, separately by kind. Everything else stays unmapped; no timing
proximity heuristics are used. Also report build actions with a direct request
over total build actions, which is a different denominator.

Go's graph provides package/dependency records after the build. TimeStart and
TimeDone bracket an action, including cache traffic, not necessarily compiler
execution. CmdReal/CmdUser/CmdSys summarize real commands without exact
per-command start/end or a stable live event stream. Requests outside the
package-action graph need more attribution. The graph is a debug interface,
not a version-stable provider API.

A real integration needs versioned provider events carrying the full action
key, package identity, dependencies, cache lookup outcome and real execution
start/end; Dagger must record spans and immutable outputs with those events.
The final executable is returned separately, since this GOCACHEPROG path does
not cache ordinary linked executables. Merely uploading these offline graph
records would add visibility, not make Go's actions Dagger-scheduled calls.
Whether that meets "native per-package" remains the decision after this spike.

## Functional evidence (not timing evidence)

One functional yq run passed through the approved Cloud path because the
development host exceeded load1=16. Go 1.26.6, CGO disabled, build parallelism 4,
copy materialization, helper latency 1 ms. The helper's race-enabled tests also
passed. An earlier attempt failed because the Cloud engine ran out of disk;
the authorized cache prune resolved that, and the failed attempt is excluded.

| Phase | Gets (hits / misses) | Puts | Put body bytes | Materialized bytes | Actions with commands, plain / helper |
|---|---|---:|---:|---:|---|
| First | 484 (0 / 484) | 1,010 | 147,757,568 | 147,757,231 | 264 / 264 |
| Edit A | 747 (742 / 5) | 9 | 6,230,944 | 147,738,977 | 3 / 3 |
| No change | 749 (748 / 1) | 1 | 42,797 | 147,751,407 | 0 / 0 |

Each phase also has one close request; there were no protocol errors. Both
edited binaries printed yq v4.49.2. The no-change metadata put is not evidence
of a compiler running: the graph reports zero commands.

| Phase | Direct request matches | Direct + known subkeys | Build actions with a direct request |
|---|---|---|---|
| First | 527/1,494 (35.27%) | 1,054/1,494 (70.55%) | 263/263 |
| Edit A | 266/756 (35.19%) | 532/756 (70.37%) | 263/263 |
| No change | 262/750 (34.93%) | 524/750 (69.87%) | 262/263 |

No ambiguous matches occurred. Recognized subkeys were stdout, link-stdout and
srcfiles. Roughly 30% of requests remain unattributed by this method. The graph
does supply all package nodes and dependency edges; request coverage is a
different question from graph coverage. Approximately 148 MB copied on each
warm invocation is an observed byte count and a reason to test the data path,
not a timing conclusion or a prediction of engine-store performance.

Trace: https://dagger.cloud/dagger/traces/e5493e5568dfb7d55f7d6fd1a44e7481
Raw check artifacts on the development host:
\`~/g24-scratch/codex/spike7-functional-2/run/\`. The VM harness does not depend
on those files. The Go files tested there are the committed prototype; the
artifact's harness_commit predates the commit because the check ran before it.
