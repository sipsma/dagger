# Spike 5: immutable archive views and function file transport

This is an engine experiment based on dagger/dagger 49065f5de3. It is not a
product API. Enable `_DAGGER_SPIKE5=1` only on the fresh dev engine created by
the harness. No cache volumes are used as inputs or outputs under test. The
dev engine itself has a fresh state volume, with normal GC enabled.

Risk: archive assembly, mount setup and output publication may consume the
entire proposed few-millisecond call boundary; this affects per-package Go
builds and other modules passing many immutable tool outputs.

## Mechanisms

`Container.withSpike5FileView(files, paths)` installs all files in one resolver
as ordinary read-only bind mounts at the supplied paths. It copies no archive
bytes. It reopens snapshot refs for ownership and uses the existing executor.
This is deliberately a conservative implementation: a link with 1,400 files
requires 1,400 mounts. It does not predict FUSE or an optimized kernel view.
Paths are restricted to `/.spike5/view/`, with duplicates and parent/child
collisions rejected. The files are read-only; their parent directories remain
part of the private writable container root. A fully read-only directory view
would also need a metadata tree. Services from input files are retained.

For direct input binding, functions with `spike5Bind=true` get their
`spike5Dir` and `spike5File` arguments at `/.spike5/input` and `/.spike5/file`.
Both mounts are read-only. These are reserved argument conventions for the
spike, not new pragmas. The ordinary function call still includes the original
object IDs and flag in its cache key. The experiment does not expose any
other caller files or reuse a live process.

For sealed output, `spike5Output=true` reserves a private writable directory
mount at `/.spike5/output`. The function writes `result`, sends the reserved
return token through ordinary `ReturnValue`, waits for its acknowledgement,
and exits. After exec completion, the engine validates a regular file, reopens
the committed immutable snapshot, computes the content digest and returns a
normal `File` result under the function call. There is no filesync import.
Both output arms send ReturnValue explicitly and use `os.Exit`, so skipped
SDK cleanup cannot be credited to sealing. They do not run deferred cleanup;
a real SDK must carry a typed output token through its normal dispatcher.
Absolute output-arm times therefore need not match step 4's normal SDK exit.

The functional check writes the final bytes *after* `ReturnValue` acknowledges,
and checks that those bytes are captured. It also rejects a missing file, a
symlink and nonzero process exit. The return acknowledgement never waits for
capture. Output capture is not attempted after a failed exec.

## What is measured

| Pair | Work included |
|---|---|
| `withfiles` / `view` | Fresh assembly or view, mount setup, one exec reading every byte, SHA-256, stdout publication |
| `export-dir` / `bind-dir` | A Go self-call, directory export or direct binding, reads and hashes of all files, scalar result |
| `export-file` / `bind-file` | The same for one File argument |
| `workdir` / `sealed` | A Go self-call writing a file, workdirFile import or sealed capture, caller File.sync |
| `exec-two` / `exec-one` | Existing exec plus Directory.sync then File.sync, or direct File.sync |

`exec-one` is a control, not an equivalent content-addressed sealed output:
selecting a file from a pending exec does not install the eager content digest
that directory selection after sync can install. The module output arms both
write the salt into an otherwise zero-filled buffer. The exec controls write
zeros with a shell command. Compare paired arms, not absolute output costs
between languages as though their workloads were identical.

Every measured call has a fresh salt. For views and withFiles it changes the
visible directory, preventing assembly hits. Inputs are prepared separately:
one seed is copied once into each archive's independent immutable snapshot.
The resulting files are synced before the measured loop. Directory inputs are
assembled once before timing; the file-view pair measures assembly every time.
The fixture has uniform synthetic bytes, not actual Go archive structure.
Snapshot sharing, compression and real linker access patterns remain unknown.

`prepare_ms` reports preparation separately; the first occurrence includes
fixture creation and later occurrences reuse cached fixtures. Module loading,
engine build and image downloads are outside the loop and remain visible in
the complete slot log. No target Go build is being benchmarked here.

Serial calls and fan-out include all client/engine work between call start and
completion. Fan-out worker durations overlap: do not sum them into wall time.
Report wall_ms/n as throughput cost separately from p50 latency. Payload reads
and hashing are identical within each pair. Inspect wcprof for which phase
changed; a faster end-to-end result alone does not prove copies dominated.

## Functional validation and timing slot

Run from a clean checkout. Bash, git, coreutils and a Dagger CLI are required.
Use only the authorized installed CLI on the development host. On linux-vm,
`DAGGER_BIN` can point to its CLI. Each invocation records its resolved path
and version. The runner checks load1 <= 16 and never falls back to cloud.

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
bash hack/gocache/g24/spike5/run.sh "$HOME/g24-spike5-functional-$(date -u +%Y%m%dT%H%M%SZ)" functional
```

The functional check verifies equal bytes with 5, 30, 260 and 1,400 independent
1-KiB archive snapshots, read-only bindings, changed input visibility, 1-MiB
outputs, lifecycle and failure behavior, plus each measurement arm at fan-out.
It reports no elapsed times and supports no local timing inference.
It also captures one functional profile and runs the exact report-generation
pipeline used by timing mode. Those reports validate parsing and class filters;
their durations are not benchmark evidence. `results.jsonl` contains one JSON
record per line in both modes.

On linux-vm only:

```sh
export DAGGER_LEAVE_OLD_ENGINE=true
timeout --foreground 30m bash hack/gocache/g24/spike5/run.sh "$HOME/g24-spike5-$(date -u +%Y%m%dT%H%M%SZ)" timing
```

Three rounds, serial and fan-out (cap 8), reverse pair order in round 2:

- View versus withFiles: 5/30/260/1,400 archives, each 512 KiB. Twelve calls per
  arm at 5/30; four at 260/1,400 (effective fan-out 4 there).
- File binding and output pairs: 1 KiB and 1 MiB, 30 calls per arm.
- Directory binding: 5/30 archives at 512 KiB, twelve calls per arm.

That is 144 cases and 2,832 calls. Each case has a 120-second deadline. The
inner series stops after its 25-minute budget; the outer slot allows at most
30 minutes including engine build. Timeout/failure is incomplete evidence,
never a timing sample. The deadline belongs only to processes this run starts.
Large withFiles cases may retain tens of GB; keep normal engine GC enabled.

Round 1 writes wcprof dumps. They include the Run function's preparation as
well as its measured loop; class totals are not disjoint loop-time budgets.
Use the individual consumer calls/execs when attributing per-call costs.
Raw logs and dumps stay in the output directory.
Return that directory's absolute path and copy only `host-meta.txt`,
`run/dev-version.txt`, `run/summary.txt`, `run/results.jsonl`, `run/exitcode`,
`run/PASS` and text profile reports into `.g24-results/<series>/` in the runner's
worktree, preserving paths. If incomplete, copy the listed files that exist
and say which case failed. Keep everything else in the original VM directory.

Report per-pair first/second arm medians and ratios across the three rounds,
serial per-call latency, fan-out wall/n, preparation, bytes and counts. Inspect
copy, mount, exec lifecycle, filesync, content hash and result-publication time.
No-op cached calls are not a target of this experiment.

## Remote cache representation

These options use Dagger results, not a separate engine-local store. The file
view is a persistable `Container` result: its metadata records the ordered
paths and read-only flags, and each archive is an ordinary snapshot part. It
does not invent a single byte digest for the complete view. Its call identity
includes the parent, ordered File IDs and paths; the cache's snapshot payloads
are content-addressed and use the existing Container transfer codec.

Direct binding accepts ordinary File/Directory results and uses the existing
mount operations. Evaluation can materialize remotely acquired parts before
the function starts. Sealed output is an ordinary File result with an eager
content digest and its own committed snapshot reference. Default-cached module
functions are persistable; the File codec describes `/result` and the snapshot
part that the remote cache transfers. No caller-local pathname or function
container has to survive after capture.

The focused codec check exercises the actual view builder and the same
snapshot-backed File shape as sealing: all archive bytes have a transfer part,
and normalized foreign metadata contains no producer-local snapshot IDs.
This is representation-level validation, not an observed network push/pull.
Real remote upload, lookup, pull, restart and remote eviction are not tested by
this series. Those are required validation for a shipping feature and the
next engine-store spike; local transport savings must not be reported as remote
cache savings. Keep experimental results in a separate cache namespace.

## Deliberate omissions

- No new persistence format. Results use the existing persistable types and
  transfer codecs, but real restart/remote export/import is unverified here.
  The series runs on a fresh dev engine without a remote-cache timing arm.
- GC ownership uses reopened snapshot refs; this is not a long-session pruning
  or concurrent eviction stress test. No GC settings are changed.
- File-view identity uses ordered file IDs and visible paths. It does not
  normalize permutations or define a general path/conflict API.
- Direct binding supports only the two top-level argument conventions, not
  nested objects, lists, optional bindings, permission remapping or services
  as a complete API. Existing mount machinery remains in the measured path.
- A sealed result owns its output snapshot; the prototype does not enforce an
  otherwise empty output directory, handle multiple outputs, or support
  symlinks. It retains ordinary File metadata/content hash semantics.
- No prepared launcher, protocol envelope optimization, scheduler change or
  persistent module process. Those costs remain in every measured call.
