# Runs inside the client container. Plain cold `go build` (gcexp plain, no cache volume) vs the cold
# per-package replay (concurrency 16), alternating in ABBA order, each with a fresh salt.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ')" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
t warm -s call gcexp replay --src ./yq --salt warm-$NONCE --concurrency 16 --nonce w
t warm-plain -s call gcexp plain --src ./yq --volume "" --salt warmp-$NONCE --nonce w
k=0
for m in ${ORDER:-plain replay replay plain}; do
  k=$((k+1))
  curl -sf "$D?flush=1" -o /dev/null
  if [ "$m" = plain ]; then
    t cold$k-plain -s call gcexp plain --src ./yq --volume "" --salt p$k-$NONCE --nonce n$k
  else
    t cold$k-replay -s call gcexp replay --src ./yq --salt r$k-$NONCE --concurrency 16 --nonce n$k
  fi
  curl -sf "$D?flush=1" -o /out/cold$k-$m.dump
done
