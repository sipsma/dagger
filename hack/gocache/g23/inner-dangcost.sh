# g22: what the remaining Dang parse costs on the critical path of a yq build with today's official
# Go module (github.com/dagger/go after dagger/go#66). Runs in the client container; /work is the
# gcexp workspace with yq at ./yq. Needs an engine with the g22 dang.* wcprof timing points.
# Steps, each one whole dagger invocation of `go | packages | get . | binary | size`:
#   preload (module load, scanner helper, base image, yq's module downloads), first (cold go-build
#   volume), noop-1..3 (nothing changed; noop-3 is profiled), edit (edit A, profiled),
#   noop-after-edit (profiled), then a 20 s engine CPU profile over back-to-back no-change builds.
set -e
export DAGGER_LEAVE_OLD_ENGINE=true
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
GOMOD=${GOMOD_REF:-github.com/dagger/go@495f94ef2895e6c6925732896e34412c19c4a116}
echo "module $GOMOD" >> /out/summary.txt
mkdir -p /rm && cp -r /work/yq /rm/yq
(cd /rm/yq && git init -q && git add -A && git -c user.email=g22@x -c user.name=g22 commit -qm yq)
printf '[modules.go]\nsource = "%s"\n' "$GOMOD" > /rm/yq/dagger.toml
cd /rm/yq
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-120)" >> /out/summary.txt; }
B='go | packages | get . | binary | size'
t preload -s -c 'go | packages | keys'
t preload-mod -s -c 'go | module . | base | with-directory . $(go | module . | source) | with-exec go,mod,download | sync'
flush; t first -s -c "$B"; dump first
t noop-1 -s -c "$B"
t noop-2 -s -c "$B"
flush; t noop-3 -s -c "$B"; dump noop-3
f=pkg/yqlib/operator_add.go
if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $f)" != 1 ]; then echo "edit-a: expected one match in $f" >> /out/summary.txt; exit 0; fi
sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $f
flush; t edit -s -c "$B"; dump edit
flush; t noop-after-edit -s -c "$B"; dump noop-after-edit
curl -sf "http://dagger-engine:6060/debug/pprof/profile?seconds=20" -o /out/dang-cpu.pprof &
prof=$!
i=0
while kill -0 $prof 2>/dev/null; do i=$((i+1)); t cpu-noop-$i -s -c "$B"; done
wait $prof || echo "dang-cpu.pprof: capture failed" >> /out/summary.txt
echo "dang-cpu.pprof spans $i no-change builds" >> /out/summary.txt
flush
