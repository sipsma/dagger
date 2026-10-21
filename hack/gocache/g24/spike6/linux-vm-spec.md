# Spike 6 linux-vm spec

Branch: `gocache-g24-spike6-prepared` on `https://github.com/sipsma/dagger.git`.
Base: `494b3afa0e`. Use the final commit supplied with this spec.

Risk: a pool can hide launch latency while retaining all preparation CPU and
memory, or SDK startup can give persistence a larger advantage than expected;
this affects module calls and short execs across Dagger.

The series builds and uses a **fresh dev engine from this checkout**, with
`_DAGGER_SPIKE6=1`, wcprof and normal GC. The CLI's default engine only builds
and hosts it. Never use cloud. The engine state volume is unique to the series;
no workload depends on cache volumes. Prepared environments are disposable
performance details, never inputs. Results retain Dagger's ordinary immutable
cache path. Remote transfers are neither changed nor measured here.

## Invocation and order

On a load1 <=16 slot, use a clean checkout and no other work in the dev engine:

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
git fetch https://github.com/sipsma/dagger.git refs/heads/gocache-g24-spike6-prepared
git switch --detach FETCH_HEAD
out="$HOME/g24-spike6-$(date -u +%Y%m%dT%H%M%SZ)"
timeout --foreground 30m bash hack/gocache/g24/spike6/run.sh "$out" timing
```

Run **one series**, three rounds. Reserve a 20–30 minute slot including the
engine/CLI build; the inner series has a 25-minute hard cap. This is a budget,
not a local runtime prediction. If it times out, preserve results and report
incomplete; do not silently reduce counts or treat partial data as three rounds.
No files from the development host are needed. CLI resolution is `DAGGER_BIN`,
then `$HOME/.local/bin/dagger`, then PATH. Its resolved path/version, source
commit and host load are recorded. Development-host checks use only its installed
`~/.local/bin/dagger`.

Each round uses serial and eight-way fan-out, with 256 calls per case:

- Engine cases: `true` (no mounts), small shell exec with file I/O, generated Go SDK runtime, same runtime with
  telemetry startup/shutdown skipped; each unprepared and prepared.
- Pipe startup controls: same generated binary, fresh and persistent, each full
  and trimmed, using the same envelope and file I/O.

Round 2 reverses each pair; rounds 1 and 3 use unprepared/fresh first. There are
72 cases: 12,288 engine executions and 6,144 pipe messages, plus 24 explicitly
unmeasured engine warmups and module/schema build work. The module binary is
built/exported once before the cases and its SHA-256 is recorded. Inputs and
code are hot for this launch microbenchmark; it makes no cold Go-build claim.
The pool has at most `c` one-use preparations underway and charges each refill;
serial ready latency is not a steady-state throughput claim.

## What to report

Read README.md before interpreting the figures. Report medians of each round's
per-call medians and case totals, keeping serial and fan-out separate:

1. Unprepared `total_ms`; prepared `prepare_ms`, `ready_to_result_ms`,
   `release_rtt_ms`, and `total_ms`. The release request is included in ready
   latency; its RTT overlaps that latency. Report total case wall time too.
2. CPU deltas for the entire dev-engine parent cgroup, including sibling exec
   groups, and separately client CPU. Report memory before, after, sampled peak,
   and ready-container readings. Inspect the charged cgroup path in meta.json;
   an unexpected layout fails rather than silently charging engine CPU alone.
   Memory sampling can miss short peaks; do not claim exact pool RSS from a
   cumulative peak. The raw kernel memory.peak is lifetime-scoped.
3. Fresh versus persistent pipe response times and times including fresh exit;
   also all worker startups, persistent shutdown and total case wall/child CPU.
   Report ready-process RSS samples and lifetime maximum child RSS (not summed
   concurrency). The persistent warm interval excludes its one-time startup,
   but the total includes it. Full/trimmed SDK arms differ only in telemetry
   setup/close. They include the same generated SDK package initialization.
4. wcprof lifecycle/query/exec summaries, confirming actual execs occurred.
   Prepared `processSpawn` includes the deliberate gate wait. Do not interpret
   this as launch overhead, or add/subtract overlapping medians. The profile
   includes module preparation as well as cases; use JSON rows for comparisons.
5. Whether ready-path savings survive charged preparation/refill, how CPU/memory
   change, and whether the measured **startup-only** persistent advantage warrants
   revisiting the fresh-process default. This control is not an isolation-safe
   persistent Dagger executor and not today's full ~33 ms self-call transport.

No hypothesis that this saves 4–10 ms is assumed in the report. The prototype
retains crun setup; it cannot show that zygote/fork or checkpoint designs would
have the same CPU cost. It cannot prepare unknown dependency inputs or rebind
them after launch. A serial package chain may therefore have no preparation
lead time. Do not turn ready-call savings into a Go-build savings projection.
These are distinct future experiments.

## Small files to return

Create `.g24-results/<series>/` in the runner's worktree. Copy only:

- `host-meta.txt`;
- `run/summary.txt`, `run/comparison.txt`, `run/results.jsonl`, `run/meta.json`,
  `run/dev-version.txt`, `run/exitcode`, and `run/PASS` if present;
- `reports/series.reports.txt`.

Preserve relative paths. Keep raw profile dumps, resource samples, binaries,
worker directories, invocation script and full logs in the output directory on
linux-vm. On failure, return exitcode and the short error tail too; missing PASS
means incomplete/failed. Do not delete or prune any other engine.
