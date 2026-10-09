# Runs inside the client container (from-source CLI, bound to the dev engine as dagger-engine).
# /work = gcexp workspace. A latency-bound workload: `gcexp chain --n 100`, 100 sequential execs, each
# depending on the previous one. One warm-up, then 4 cold chains with fresh salts, each with a wcprof dump.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-120)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
t warm -s call gcexp chain --n 100 --salt warm-$NONCE --nonce w
for k in 1 2 3 4; do
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-chain -s call gcexp chain --n 100 --salt c$k-$NONCE --nonce n$k
  curl -sf "$D?flush=1" -o /out/cold$k-chain.dump
done
