# g23 scale series: the gcexp replay building dagger's own CLI (./cmd/dagger, ~1,418 packages: 245
# standard library, ~1,041 dependency, ~132 workspace) in a few design modes, with plain go build
# references, on one fresh dev engine. Runs in the client container; /work is a g23 gcexp prototype
# checkout, /d the dagger/dagger source (run.sh's DSRC).
# Steps: plain-cold / plain-noop (fresh GOCACHE volume; noop forced); per mode M: M-first, M-noop;
# edit D (a string in an unexported function of internal/cloud/auth, which 45 workspace packages
# import, 8 directly): plain-editD, M-editD; then edit T on top (a string in internal/cmd/dagger,
# one package below the main package): plain-editT, M-editT. wcprof dumps for every first and edit.
# G23_ARGS=fwd runs the modes in the listed order, rev in reverse.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 2400 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(grep -m1 -E '^(replay|plain)' /out/$n.txt | cut -c1-110)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
modes=${MODES:-base memo,hdr memo,hdr,prio}
if [ "${G23_ARGS:-fwd}" = rev ]; then modes=$(echo $modes | tr ' ' '\n' | tac | tr '\n' ' '); fi
echo "order ${G23_ARGS:-fwd}: $modes" >> /out/summary.txt
cp -r /d /work/dsrc
P="-s call gcexp plain --src ./dsrc --pkg ./cmd/dagger"
V=gocache-g23-scale-$NONCE
r() { n=$1; m=$2; s=$(ms); set -- -s call gcexp replay --src ./dsrc --pkg ./cmd/dagger --version-args=--help --salt "$(echo $m | tr ',' '-')-$NONCE" --concurrency 16 --stamps=false --nonce "$n"; [ "$m" != base ] && set -- "$@" --mode "$m"; timeout 2400 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(grep -m1 '^replay' /out/$n.txt | cut -c1-110) :: $(grep -m1 TIMING /out/$n.txt | sed 's/.*mode=/mode=/' | cut -c1-80)" >> /out/summary.txt; }
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
flush; t plain-cold $P --volume $V --salt pv-$NONCE --nonce pc
t plain-noop $P --volume $V --salt pv-$NONCE --nonce pn
for m in $modes; do
  tag=$(echo $m | tr ',' '-')
  flush; r $tag-first $m; dump $tag-first
  r $tag-noop $m
done
edit() { # $1 = name, $2 = file, $3 = sed expression
  if [ "$(grep -c "$4" dsrc/$2)" != 1 ]; then echo "$1: expected one match in $2" >> /out/summary.txt; exit 0; fi
  sed -i "$3" dsrc/$2
  flush; t plain-$1 $P --volume $V --salt pv-$NONCE --nonce p$1
  for m in $modes; do
    tag=$(echo $m | tr ',' '-')
    flush; r $tag-$1 $m; dump $tag-$1
  done
}
edit editD internal/cloud/auth/auth.go 's/could not acquire lock on %s: timed out/could not acquire the lock on %s: timed out/' 'could not acquire lock on %s: timed out'
edit editT internal/cmd/dagger/artifacts.go 's/addresses select different workspaces: %s and %s/addresses select two different workspaces: %s and %s/' 'addresses select different workspaces: %s and %s'
