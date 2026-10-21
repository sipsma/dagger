# Spike 6: prepared, single-use OCI containers

This is a gated experiment, not a reusable executor. `_DAGGER_SPIKE6=1` on
an ephemeral dev engine enables a readiness/release endpoint on its debug
port. `_DAGGER_SPIKE6_TOKEN` on an exec asks the injected init to wait before
starting the user process. The engine removes that control variable from the
command's environment. Declared command, stdin, mounts, network and writable
snapshot are fixed **before** preparation. Every environment is used once.
The existing crun and clean-mount-namespace path are unchanged.

This measures a ready-container pool: it moves preparation earlier, and cannot
reduce total preparation CPU by itself. It does not implement a fork server,
checkpoint/restore, dynamic input rebinding or a faster snapshot clone. It
cannot prepare a call before its declared input objects are known. The harness
charges preparation for every invocation; no free refill or amortized warmup
is hidden in its ready-call latency. On a serial compile chain, an unknown
dependency output prevents this preparation, so ready-call savings alone are
not evidence of equivalent Go-build critical-path savings. It starts up to `c` preparations at once,
releases each when ready, and replaces it after completion; no speculative
extra containers are made. Functional mode checks that private root writes do
not survive to another call, checks outputs, and requires zero parked gates
at the end. It does not replace security testing of the existing executor.

## Comparisons

1. **Engine path:** today's withExec versus the same exec parked just before
   spawning its user process. Includes `true` without mounts, a small shell
   workload with file I/O, a real generated
   Go SDK module runtime, and the same runtime with telemetry initialization and
   shutdown skipped. Serial and eight-way fan-out. The I/O arms read the same declared
   1 KiB file; Go arms write and remove the same output and return the same hash.
   Go calls use an experimental stdin JSON envelope and stdout result in both
   arms, bypassing CurrentFunctionCall queries and ReturnValue equally.
   Thus their absolute times are **not** today's ~33 ms self-call baseline.
2. **Process startup control:** export that exact module binary once, then drive
   it with pipes inside the client container. A fresh process for each message
   versus one persistent dispatcher per worker; same envelopes, input and output
   file I/O, generated `invoke`, and binary. Full SDK package initialization is
   present in both. Persistent telemetry initializes once, fresh initializes
   each time. A separately reported trimmed variant skips only telemetry init
   and close; package initialization and generated SDK stay intact.

The pipe experiment is a startup control, **not an isolated Dagger call**.
Fresh children have separate memory but share their worker's filesystem and
namespaces; persistent children additionally retain memory. The workload removes
its output in both arms. Engine-path calls supply the actual snapshot and
namespace isolation. Do not add the pipe difference to engine savings: these
measure overlapping process-start costs. This setup can test the persistent
process's startup advantage, but not claim a complete persistent-call latency.

`ready_to_result_ms` includes the HTTP release request, fresh child startup,
work, init/crun teardown, engine result handling and GraphQL response. It excludes
preparation and readiness polling; `total_ms` and case `wall_ms` include them.
`release_rtt_ms` overlaps `ready_to_result_ms`, so do not add or subtract medians.
Preparation includes readiness polling at 1 ms intervals. Prepared wcprof
`exec.processSpawn` includes the deliberate barrier wait, so it is not directly
comparable to unparked `processSpawn`. No local timing conclusions are allowed.

## Cost accounting and limits

Engine cases sample the dev engine's **parent** cgroup, including sibling exec
groups, every 20 ms and at each prepared barrier. CPU deltas include preparation, refill and cleanup, plus
engine work and the control/profiling instrumentation. Memory reports before,
after and sampled peak; the raw lifetime kernel peak is also retained. Sampling
can miss short peaks. The endpoint rejects an unexpected cgroup layout instead
of silently accounting engine CPU alone. This is an exclusive dev-engine series;
other work in that cgroup would invalidate attribution. The outer client is
outside that boundary. The pipe control reports child CPU (including waits for
all exits) and the runner's lifetime maximum child RSS, explicitly not the sum
of simultaneous processes. Preparation, startup and final dispatcher shutdown
are charged to its total wall time. Python/HTTP control overhead is included in
latency; client CPU is reported separately from engine CPU. The pipe control also
records each child's RSS at its ready handshake; this is a point sample, not
peak or aggregate concurrent RSS.

The gates are held only while the normal exec runs, are removed on completion
or cancellation, and are not persisted, remotely exported or result inputs.
The normal immutable Container result and existing cache/persistence path are
unchanged. A nonce in every tested call forces an actual execution. A production
pool needs cache-neutral scheduling controls, bounded capacity, timeout and
failure policies, admission control, cancellation stress tests, and eviction.
The debug endpoint is unauthenticated, token-addressed and dev-only; never enable
it on a shared production engine. TTY and NoInit execs are rejected. This spike
does not change OCI security configuration, mount visibility or GC policy.

Remote-cache compatibility is not a new execution-machinery gate here: prepared
environments are disposable performance details, not inputs. Results still use
Dagger's ordinary immutable objects. This does not validate remote pulls or
introduce an engine-local result store.

Run instructions and interpretation are in `linux-vm-spec.md`.
