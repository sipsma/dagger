# Prime session (runs in the client container; /work = gcexp workspace with yq at ./yq; outputs in /out).
# Cold replay builds the state, one no-change replay settles it, then two measured warm no-change replays
# with a CPU profile over both and a wcprof dump each, and an egraph snapshot at the end.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060
R="-s call gcexp replay --src ./yq --salt rs-$NONCE --concurrency 16"
t prime-cold $R --nonce p1
t prime-noop $R --nonce p2
curl -sf "$D/debug/wcprof/dump?flush=1" -o /dev/null
curl -sf "$D/debug/pprof/profile?seconds=60" -o /out/cpu-warm.pprof &
t warm-replay1 $R --nonce w1
curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/warm-replay1.dump
t warm-replay2 $R --nonce w2
curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/warm-replay2.dump
wait
curl -sf "$D/debug/dagql/egraph" -o /out/egraph-warm-end.json || true
