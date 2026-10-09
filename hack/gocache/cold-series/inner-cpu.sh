# CPU budget capture (runs in the client container; /work = gcexp workspace with yq at ./yq).
# /proc/stat is machine-wide, so each step records the CPU-seconds the whole VM spent busy (user, nice,
# system, irq, softirq) and idle during it: plain go build against the per-package replay, cold and serial.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
cpu() { awk '/^cpu /{printf "%d %d", $2+$3+$4+$7+$8, $5+$6}' /proc/stat; }
secs() { awk -v t="$1" 'BEGIN{printf "%.2f", t/100}'; }
t() {
  n=$1; shift; set -- $(cpu) "$@"; b0=$1; i0=$2; shift 2; s=$(ms)
  timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt
  e=$(ms); set -- $(cpu); b1=$1; i1=$2
  echo "$n wall_ms=$((e-s)) busy_cpu_s=$(secs $((b1-b0))) idle_cpu_s=$(secs $((i1-i0))) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-230)" >> /out/summary.txt
}
D=http://dagger-engine:6060/debug/wcprof/dump
P="-s call gcexp plain --src ./yq"
R="-s call gcexp replay --src ./yq"
t preload $P --volume "" --salt pre-$NONCE --nonce p
set -- $(cpu); b0=$1; i0=$2; sleep 10; set -- $(cpu)
echo "idle-10s busy_cpu_s=$(secs $(($1-b0))) idle_cpu_s=$(secs $(($2-i0)))" >> /out/summary.txt
t plain-p12 $P --volume p12-$NONCE --salt p12-$NONCE --nonce 1
t plain-p1 $P --volume p1-$NONCE --salt p1-$NONCE --nonce 2 --p 1
curl -sf "$D?flush=1" -o /dev/null
t cold1-r16c4 $R --salt r1-$NONCE --nonce 1 --concurrency 16
curl -sf "$D?flush=1" -o /out/cold1-r16c4.dump
t cold2-r16c4 $R --salt r2-$NONCE --nonce 2 --concurrency 16
t cold3-r1c12 $R --salt r3-$NONCE --nonce 3 --concurrency 1 --gc-concurrency 12
