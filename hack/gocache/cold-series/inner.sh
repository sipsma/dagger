# Runs inside the client container (from-source CLI, bound to the dev engine as dagger-engine).
# /work = gcexp workspace with yq at ./yq. Outputs in /out.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
R="-s call gcexp replay --src ./yq"
t warm $R --salt warm-$NONCE --concurrency 16 --nonce w
k=0
for c in $CONCS; do
  k=$((k+1))
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-c$c $R --salt c$k-$NONCE --concurrency $c --nonce n$k
  curl -sf "$D?flush=1" -o /out/cold$k-c$c.dump
done
