# Compile-work gap capture (runs in the client container; /work = gcexp workspace with yq at ./yq).
# Needs gcexp with the compile-work options (sipsma/dagger gocache-g19-gcexp-cw).
# Plain go build, each cold in a fresh GOCACHE volume and traced (per-package build/link action times):
#   plain-p12 (go defaults), plain-c4 (-gcflags=all=-c=4, the replay's compiler concurrency), plain-p1 (serial).
# Replays, each with per-package tool times and a wcprof dump:
#   cold1-r16c4 first build (also prints the dependency graph); then salt-only rebuilds that reuse the
#   input directories: cold2-r16c4 (default), cold3-r16c12 (-c=12), cold4-r12c4 (12 execs at once),
#   cold5-r1c12 (serial, -c=12, for a like-for-like comparison with plain-p1).
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-260)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
P="-s call gcexp plain --src ./yq"
R="-s call gcexp replay --src ./yq"
t preload $P --volume "" --salt pre-$NONCE --nonce p
# Build the one-time pack tool (cached per engine) so measured first builds exclude it.
t preload-pack -s call gcexp pack-tool
t plain-p12 $P --volume p12-$NONCE --salt p12-$NONCE --nonce 1 --trace
t plain-c4 $P --volume c4-$NONCE --salt c4-$NONCE --nonce 2 --trace --gcflags all=-c=4
t plain-p1 $P --volume p1-$NONCE --salt p1-$NONCE --nonce 3 --trace --p 1
r() { n=$1; shift; curl -sf "$D?flush=1" -o /dev/null; t $n $R "$@"; curl -sf "$D?flush=1" -o /out/$n.dump; }
r cold1-r16c4 --salt r1-$NONCE --nonce 1 --concurrency 16 --gc-concurrency 4 --graph
r cold2-r16c4 --salt r2-$NONCE --nonce 2 --concurrency 16 --gc-concurrency 4
r cold3-r16c12 --salt r3-$NONCE --nonce 3 --concurrency 16 --gc-concurrency 12
r cold4-r12c4 --salt r4-$NONCE --nonce 4 --concurrency 12 --gc-concurrency 4
r cold5-r1c12 --salt r5-$NONCE --nonce 5 --concurrency 1 --gc-concurrency 12
