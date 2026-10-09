# dagger call start-up capture (runs in the client container; /work = gcexp workspace).
# Two warm-up calls load the module and cache `gcexp chain --n 1`. Then five timed start-ups
# (start1.dump profiles the first), then a 15 s engine CPU profile over back-to-back start-ups
# (startup-cpu.pprof; the summary says how many it spans). Every call after the first warm-up is a
# function cache hit, so what remains is CLI start-up: session, module load and typedef loading.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 600 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-120)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
C="-s call gcexp chain --n 1 --salt s-$NONCE --nonce n"
t warm1 $C
t warm2 $C
curl -sf "$D?flush=1" -o /dev/null
t start1 $C
curl -sf "$D?flush=1" -o /out/start1.dump
for i in 2 3 4 5; do t start$i $C; done
curl -sf "$D?flush=1" -o /dev/null
curl -sf "http://dagger-engine:6060/debug/pprof/profile?seconds=15" -o /out/startup-cpu.pprof &
prof=$!
i=0
while kill -0 $prof 2>/dev/null; do i=$((i+1)); t cpu-start-$i $C; done
wait $prof || echo "startup-cpu.pprof: capture failed" >> /out/summary.txt
echo "startup-cpu.pprof spans $i start-ups" >> /out/summary.txt
curl -sf "$D?flush=1" -o /dev/null
