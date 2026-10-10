# Engine CPU profile across no-change calls of today's real Go module (github.com/dagger/go main)
# on yq, to attribute the time between engine ops inside its module calls. Runs in the client
# container; /work is the gcexp workspace with yq at ./yq.
set -e
GOMOD=${GOMOD_REF:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
mkdir -p /rm && cp -r /work/yq /rm/yq
(cd /rm/yq && git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm yq)
printf '[modules.go]\nsource = "%s"\n' "$GOMOD" > /rm/yq/dagger.toml
cd /rm/yq
dagger -s -c 'go | packages | get . | binary | size' > /out/warm.txt 2>&1
dagger -s -c 'go | packages | get . | binary | size' > /out/warm2.txt 2>&1
curl -sf "http://dagger-engine:6060/debug/pprof/profile?seconds=${PPROF_SECONDS:-20}" -o /out/cpu.pprof &
p=$!
s=$(awk '{printf "%d", $1*1000}' /proc/uptime)
n=0
while [ $(( $(awk '{printf "%d", $1*1000}' /proc/uptime) - s )) -lt $(( ${PPROF_SECONDS:-20} * 1000 - 3500 )) ]; do
  dagger -s -c 'go | packages | get . | binary | size' > /out/call-$n.txt 2>&1; n=$((n+1))
done
wait $p
echo "calls=$n" > /out/summary.txt
