# Spike 9 linux-vm series

Branch: `https://github.com/sipsma/dagger.git`, `gocache-g24-spike9-result-store`.
Use the commit supplied with this spec, based on upstream `494b3afa0e`.
The harness has no dependency on the author's scratch files.

This series uses **two fresh dev engines built from this branch**, launched by
the runner's normal outer CLI engine. The Go comparisons use engine A. Engine B
is only for the existing transfer fixture. Both have separate state, normal GC,
and no configured remote-cache endpoint or credentials. This does not use a
fresh installation of the outer CLI engine or a real remote-cache service.

## Exact invocation and budget

Use an isolated checkout at the supplied commit, with yq v4.49.2 at
`$HOME/gocache-bench/yq` (commit `7ccaf8e700ce99eb3f0f6cef7f5930a0b3c827cd`).
Require at least 30 GiB free. Do not run alongside another timing series.
Check `uptime`; reschedule if load1 exceeds 16. The harness repeats this check
immediately before its outer Dagger launch. Use the runner's installed beta.19
CLI; `DAGGER_BIN` can select its location. No host Go commands are needed.

```bash
cd /path/to/isolated/dagger-checkout
series=gocache-g24-spike9-$(date -u +%Y%m%dT%H%M%SZ)
out=$HOME/gocache-bench/$series
export DAGGER_LEAVE_OLD_ENGINE=true
export DAGGER_BIN=$(command -v dagger)
export YQ=$HOME/gocache-bench/yq
bash hack/gocache/g24/spike9/run.sh "$out" all timing 3
python3 hack/gocache/g24/spike9/report.py "$out" > "$out/report.txt"
```

One series, three paired rounds for **each** project: yq first, dagger CLI second.
The order within rounds is plain/engine, engine/plain, plain/engine. Each arm
runs cold first build, item-4 edit A (yq) or edit D (dagger), then no-change.
That is **36 target builds**, plus one producer/consumer fixture check. No
injected latency. `go build -p 16`, `GOMAXPROCS=16`, helper concurrency 64,
CGO disabled, pinned Go 1.26.6 image recorded in host-meta/meta.json. The CLI
build target is `./cmd/dagger`; yq builds `.`. Edits are literal equivalents of
the g23 item-4 replacements. A source mismatch stops the run.

Allow roughly **20–30 minutes including engine/helper compilation and module
downloads**; this is a planning estimate, not a result. The new store may exceed
that budget. The script gives each project a 24-minute internal deadline and
25-minute CLI timeout to preserve failure artifacts; it is not a 30-minute
whole-series timeout. At 30 minutes, do not queue more work or silently reduce
rounds; report progress and let the in-flight bounded project finish. Preserve
partial results and say the series was incomplete if it cannot finish. No
performance conclusion requires treating a timeout as a successful build.

First-build caches are cold for the target standard library as well as project
packages. Every round has a fresh ordinary GOCACHE and a fresh engine-store
namespace. Module downloads and helper compilation happen before the measured
builds. Plain and engine runs use the same source path and flags. Edit and
no-change retain entries in engine A; local hit files are discarded after each
build, so each build pays live export costs. No cache volume backs tool entries.

## What to report

- Commit, CLI/dev-engine versions, toolchain image, source commits, host load,
  free disk, parallelism, run order, completeness and any error.
- Plain and engine wall time for all phases/rounds; median paired differences
  and ratios per project/phase. No-change is a guard, not the optimization target.
  `report.py` prints per-arm medians and individual traffic summaries; compute
  paired deltas from the same round in results.jsonl as well.
- Gets, hits/misses, puts, logical put/export bytes and GraphQL request counts.
  Get-hit/get-miss and put p50/p95 latency; nonempty File.export p50/p95 and bytes.
  `PutNS` includes actual filesync import/digest/publication. The initial lazy
  Host.file ID request is separate (`ImportNS`); report it too. Helper/body/queue
  and response intervals help locate excess cost. Never sum overlapping helper
  intervals into build wall time. GraphQL byte counts exclude filesync wire
  framing; logical payload counts are explicit.
- Package match fractions and ambiguous/unmapped counts, plus offline analysis
  time. Graph emission is inside both arms; no exact compiler spans are claimed.
- Producer and consumer fixture reports: missing entry before publication/import,
  both original keys hit after import, identical bytes, no consumer puts, and
  positive provider-read/installed-chain counts. Real service lookups/pulls are
  **unmeasured**; fixture acquisition latency is not remote-network evidence.

Compare engine-arm overhead to spike 7 only with the toolchain/parallelism and
measurement boundaries in view. Do not attribute the entire difference to RPC
round trips: this arm also imports, hashes, persists and exports real snapshots.

## Return only small files

Copy the following into `.g24-results/$series/` in the runner's worktree,
preserving relative paths:

```bash
mkdir -p ".g24-results/$series"
(cd "$out" && find . -type f \( \
  -name host-meta.txt -o -name report.txt -o -name summary.txt -o \
  -name results.jsonl -o -name meta.json -o -name exitcode -o -name PASS -o \
  -name dev-version.txt -o -name analysis.json -o \
  -name fixture-publish.json -o -name fixture-consume.json \
\) -print0 | tar --null -T - -cf -) | tar -xf - -C ".g24-results/$series"
```

Everything else stays under `$out` on linux-vm: full engine/CLI logs, binary,
received fixture payloads, raw request logs, action graphs, per-request/package
mappings and copied Go source excerpts. If something fails, report the precise
error and its path rather than copying large logs into the result tree.

## Gate and risk

The store uses content-addressed, engine-cached Dagger File results; fixtures
exercise transfer into a second fresh engine. **The real remote-cache gate is
unverified:** no real remote lookup, pull, remote index service or cross-engine
publication race is tested. Option (b) authorizes this narrower evidence.

Risk and who: per-entry import, hashing and live File.export may dominate warm
edits for tools using their own scheduler; Go users and any future generic tool
cache client bear that cost.

If VM results make this competitive enough that remote pulls could decide the
choice, request: **an isolated supported remote-cache endpoint and scoped test
namespace credentials for two fresh dev engines, so we can measure real key
lookups and cold snapshot pulls without accessing other teams' caches.**
