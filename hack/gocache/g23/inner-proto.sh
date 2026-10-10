# g23 prototype series: the gcexp replay in several design modes on one fresh dev engine, with plain
# go build references. Runs in the client container; /work is a g23 gcexp prototype checkout with yq
# at ./yq.
# Each mode gets its own salt, so its first build is cold for every package exec; modes share only
# the preloaded module downloads, golang image, pack and header tools.
# Steps: plain-cold / plain-noop (fresh GOCACHE volume; noop forced); per mode M: M-first, M-noop;
# a warm-std first build for both (see below); then edit A; plain-edit; per mode: M-edit. wcprof dumps
# for every mode's first and edit, and the warm-std first build.
# G23_ARGS=fwd runs the modes in the listed order, rev in reverse (ABBA across slots).
# MODES overrides the list; "base" stands for driver v1 unchanged.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(grep -m1 -E '^(replay|plain)' /out/$n.txt | cut -c1-110) :: $(grep -m1 TIMING /out/$n.txt | sed 's/.*mode=/mode=/' | cut -c1-80)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
modes=${MODES:-base hdr memo memo,hdr coarse,hdr prio lazy}
if [ "${G23_ARGS:-fwd}" = rev ]; then modes=$(echo $modes | tr ' ' '\n' | tac | tr '\n' ' '); fi
echo "order ${G23_ARGS:-fwd}: $modes" >> /out/summary.txt
P="-s call gcexp plain --src ./yq"
V=gocache-g23-$NONCE
r() { n=$1; m=$2; salt=${3:-$(echo $m | tr ',' '-')}; s=$(ms); set -- -s call gcexp replay --src ./yq --salt "$salt-$NONCE" --concurrency 16 --stamps=false --nonce "$n"; [ "$m" != base ] && set -- "$@" --mode "$m"; timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(grep -m1 '^replay' /out/$n.txt | cut -c1-110) :: $(grep -m1 TIMING /out/$n.txt | sed 's/.*mode=/mode=/' | cut -c1-80)" >> /out/summary.txt; }
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
flush
t plain-cold $P --volume $V --salt pv-$NONCE --nonce pc
t plain-noop $P --volume $V --salt pv-$NONCE --nonce pn
for m in $modes; do
  tag=$(echo $m | tr ',' '-')
  flush; r $tag-first $m; dump $tag-first
  r $tag-noop $m
done
# Warm standard library: an earlier build with this toolchain left every std package cached. The
# per-package build primes with stdonly under the salt it then builds with; plain primes a fresh
# GOCACHE volume with go build std.
r stdwarm-prime stdonly stdwarm
flush; r stdwarm-first base stdwarm; dump stdwarm-first
t plain-stdwarm-prime -s call gcexp plain-std --volume $V-std --salt pw-$NONCE
flush; t plain-stdwarm-cold $P --volume $V-std --salt pw-$NONCE --nonce pwc
f=yq/pkg/yqlib/operator_add.go
if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $f)" != 1 ]; then echo "edit-a: expected one match in $f" >> /out/summary.txt; exit 0; fi
sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $f
flush; t plain-edit $P --volume $V --salt pv-$NONCE --nonce pe
for m in $modes; do
  tag=$(echo $m | tr ',' '-')
  flush; r $tag-edit $m; dump $tag-edit
done
