# Re-execution detector (runs in the client container; /work = gcexp workspace with yq at ./yq).
# One cold replay builds the state, then NOOPS (default 6) no-change replays, same salt, a fresh nonce
# each, with a wcprof dump after each: a dump's exec.processRun count above 1 means that replay
# re-executed package execs although nothing changed.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060
R="-s call gcexp replay --src ./yq --salt nz-$NONCE --concurrency 16"
t build $R --nonce b
for k in $(seq 1 ${NOOPS:-6}); do
  curl -sf "$D/debug/wcprof/dump?flush=1" -o /dev/null
  t noop$k $R --nonce n$k
  curl -sf "$D/debug/wcprof/dump?flush=1" -o /out/noop$k.dump
done
