# Runs inside the client container (from-source CLI, bound to the dev engine as dagger-engine).
# /work = gcexp workspace with yq at ./yq. Two workloads per slot, each with a wcprof dump per cold run:
# the cold yq replay at concurrency 16 (CPU-bound), then `gcexp chain --n 100` (100 sequential execs,
# latency-bound). Warm-ups first.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
t warm -s call gcexp replay --src ./yq --salt warm-$NONCE --concurrency 16 --nonce w
t warm-chain -s call gcexp chain --n 100 --salt warmc-$NONCE --nonce w
for k in 1 2; do
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-c16 -s call gcexp replay --src ./yq --salt c$k-$NONCE --concurrency 16 --nonce n$k
  curl -sf "$D?flush=1" -o /out/cold$k-c16.dump
done
for k in 1 2; do
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-chain -s call gcexp chain --n 100 --salt ch$k-$NONCE --nonce m$k
  curl -sf "$D?flush=1" -o /out/cold$k-chain.dump
done
