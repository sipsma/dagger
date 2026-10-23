# g26: what the engine charges for many read-only file mounts on one container, each file from its
# own exec's output, as dagger/go's per-file mount variant did for a package's imports. Each step
# builds a container with K such file mounts, then M directory mounts after them, then runs one
# exec; it is timed and gets a wcprof dump. If the cost per step grows with K times M, inherited
# mounts are paid again by every later link. Untimed first: the 100 source files, so the timed
# steps only mount them. G26_ARGS: "<K list> <M list>", e.g. "1,10,50,100 0,5" (empty: those).
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
set -- $G26_ARGS
ks=$(echo "${1:-1,10,50,100}" | tr , ' ')
mss=$(echo "${2:-0,5}" | tr , ' ')
max=0; for k in $ks; do [ "$k" -gt "$max" ] && max=$k; done
src() { printf "container | from alpine:3.20 | with-exec -- sh -c 'echo g26-%s-%s > /f' | file /f" "$NONCE" "$1"; }
: > /work/warm.dsh
for i in $(seq 1 "$max"); do echo "$(src "$i") | size" >> /work/warm.dsh; done
s=$(ms); dagger -s -c "$(cat /work/warm.dsh)" > /out/warm.txt 2>&1 || echo "FAILED rc=$?" >> /out/warm.txt
echo "warm wall_ms=$(( $(ms) - s )) :: $(tail -1 /out/warm.txt)" >> /out/summary.txt
step() { # $1 = K, $2 = M
  name=k$1-m$2
  c="container | from alpine:3.20 | with-env-variable G26 $NONCE-$name"
  for i in $(seq 1 "$1"); do c="$c | with-mounted-file /m/$i \$($(src "$i")) --read-only"; done
  for j in $(seq 1 "$2"); do c="$c | with-mounted-directory /d/$j \$(directory | with-new-file x $NONCE-$name-$j)"; done
  c="$c | with-exec -- true | exit-code"
  flush; s=$(ms)
  dagger -s -c "$c" > "/out/$name.txt" 2>&1 || echo "FAILED rc=$?" >> "/out/$name.txt"
  echo "$name wall_ms=$(( $(ms) - s )) :: $(tail -1 "/out/$name.txt")" >> /out/summary.txt
  dump "$name"
}
for k in $ks; do for m in $mss; do step "$k" "$m"; done; done
