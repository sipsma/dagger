# Local shape check (not for timing): gcexp memo,hdr on dagger's CLI, a first build then a no-change
# build, with a wcprof dump of each. /d is the dagger source (run.sh's DSRC).
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
cp -r /d /work/dsrc
t() { n=$1; shift; s=$(ms); timeout 2400 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(grep -m1 '^replay' /out/$n.txt | cut -c1-110) :: $(grep -m1 TIMING /out/$n.txt | sed 's/.*mode=/mode=/' | cut -c1-80)" >> /out/summary.txt; }
R="-s call gcexp replay --src ./dsrc --pkg ./cmd/dagger --version-args=--help --concurrency 16 --stamps=false --mode memo,hdr --salt ss-$NONCE"
flush; t first $R --nonce a; dump first
t noop $R --nonce b; dump noop
t noop2 $R --nonce c; dump noop2
