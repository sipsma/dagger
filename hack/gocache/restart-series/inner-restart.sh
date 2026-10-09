# Restart session on the primed state (same cache volume, engine stopped cleanly): egraph at boot, then
# three no-change replays, the first two under one CPU profile, a wcprof dump each, egraph at the end.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060
R="-s call gcexp replay --src ./yq --salt rs-$NONCE --concurrency 16"
curl -sf "$D/debug/wcprof/dump?flush=1" -o /dev/null || true
curl -sf "$D/debug/dagql/egraph" -o /out/egraph-boot.json || true
curl -sf "$D/debug/pprof/profile?seconds=60" -o /out/cpu-restored.pprof &
t restored-replay1 $R --nonce r1
curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/restored-replay1.dump
t restored-replay2 $R --nonce r2
curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/restored-replay2.dump
wait
t restored-replay3 $R --nonce r3
curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/restored-replay3.dump
curl -sf "$D/debug/dagql/egraph" -o /out/egraph-restored-end.json || true
