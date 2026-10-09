# First-build materialisation capture (runs in the client container; /work = gcexp workspace with yq at ./yq).
# A plain cold build first pulls the image, loads the module and downloads modules, so the dumped
# first replay holds only per-package work: every package exec, dependency-archive directory
# (withFiles) and source filter is new on this engine. Then one cold replay per CONCS entry (fresh
# salt) shows what a later salt-only change still materialises.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
R="-s call gcexp replay --src ./yq"
t preload -s call gcexp plain --src ./yq --volume "" --salt pre-$NONCE --nonce p
curl -sf "$D?flush=1" -o /dev/null
t first-c16 $R --salt first-$NONCE --concurrency 16 --nonce f
curl -sf "$D?flush=1" -o /out/first-c16.dump
k=0
for c in $CONCS; do
  k=$((k+1))
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-c$c $R --salt c$k-$NONCE --concurrency $c --nonce n$k
  curl -sf "$D?flush=1" -o /out/cold$k-c$c.dump
done
