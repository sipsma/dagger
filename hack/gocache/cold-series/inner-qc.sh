# Per-request query handling capture (runs in the client container; /work = gcexp workspace with yq at ./yq).
# Profiled replays: first build (first-c16.dump), no change (noop1.dump), edit A (edit-a.dump).
# Between noop1 and edit A, a 20 s engine CPU profile (noop-cpu.pprof) spans back-to-back no-change
# replays (noop-prof-*), whose wcprof events are flushed and dropped. A plain preload first pulls the image,
# loads the module and downloads modules; then the one-time pack tool is built.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
t() { n=$1; shift; s=$(ms); timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt; }
D=http://dagger-engine:6060/debug/wcprof/dump
R="-s call gcexp replay --src ./yq --salt r-$NONCE --concurrency 16"
P="-s call gcexp plain --src ./yq"
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
curl -sf "$D?flush=1" -o /dev/null
t first-c16 $R --nonce f
curl -sf "$D?flush=1" -o /out/first-c16.dump
t noop1 $R --nonce n1
curl -sf "$D?flush=1" -o /out/noop1.dump
curl -sf "http://dagger-engine:6060/debug/pprof/profile?seconds=20" -o /out/noop-cpu.pprof &
prof=$!
i=0
while kill -0 $prof 2>/dev/null; do i=$((i+1)); t noop-prof-$i $R --nonce np$i; done
wait $prof || echo "noop-cpu.pprof: capture failed" >> /out/summary.txt
echo "noop-cpu.pprof spans $i no-change replays" >> /out/summary.txt
curl -sf "$D?flush=1" -o /dev/null
# Failures below are recorded, not fatal, so the dumps above are still exported.
f=yq/pkg/yqlib/operator_add.go
if [ "$(grep -c 'unable to parse duration \[%v\]: %w' $f)" = 1 ]; then
  sed -i 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' $f
  t edit-a $R --nonce ea
  curl -sf "$D?flush=1" -o /out/edit-a.dump || echo "edit-a: dump failed" >> /out/summary.txt
else
  echo "edit-a: expected one match in $f; edit skipped" >> /out/summary.txt
fi
