# g22: where one executed Dang function call's fixed cost goes, on today's real Go module
# (github.com/dagger/go main) building yq with nothing changed. Runs in the client container; /work is
# the gcexp workspace with yq at ./yq. Needs an engine with the dang.* wcprof timing points.
# Steps: warm (first full build), then three no-change `go | packages | get . | binary | size` calls
# (binary-3.dump profiles the third), then a 20 s engine CPU profile over back-to-back no-change calls
# (dang-cpu.pprof; the summary says how many it spans).
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
GOMOD=${GOMOD_REF:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
mkdir -p /rm && cp -r /work/yq /rm/yq
(cd /rm/yq && git init -q && git add -A && git -c user.email=g22@x -c user.name=g22 commit -qm yq)
printf '[modules.go]\nsource = "%s"\n' "$GOMOD" > /rm/yq/dagger.toml
cd /rm/yq
t() { n=$1; shift; s=$(ms); timeout 1500 "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-80)" >> /out/summary.txt; }
B='go | packages | get . | binary | size'
t warm dagger -s -c "$B"
t binary-1 dagger -s -c "$B"
t binary-2 dagger -s -c "$B"
flush
t binary-3 dagger -s -c "$B"
dump binary-3
curl -sf "http://dagger-engine:6060/debug/pprof/profile?seconds=20" -o /out/dang-cpu.pprof &
prof=$!
i=0
while kill -0 $prof 2>/dev/null; do i=$((i+1)); t cpu-binary-$i dagger -s -c "$B"; done
wait $prof || echo "dang-cpu.pprof: capture failed" >> /out/summary.txt
echo "dang-cpu.pprof spans $i no-change calls" >> /out/summary.txt
flush
