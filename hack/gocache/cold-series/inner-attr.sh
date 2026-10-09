# Per-exec lifecycle attribution (runs in the client container; /work = gcexp workspace with yq at ./yq).
# Needs an engine with the g18 attribution experiment (/debug/cpu, exec.cleanup:* and exec.processExit.*
# phases). After the preloads: a cold plain go build (the containers' CPU baseline), the first replay at
# c16 and two salt-only cold replays at c16, each with a wcprof dump. Around each step it records the
# engine's cumulative CPU (/debug/cpu): the engine process, the engine's cgroup (engine plus the runtime
# processes it starts) and the execs' cgroup (every container), as per-step deltas in ms.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
C=http://dagger-engine:6060/debug/cpu
D=http://dagger-engine:6060/debug/wcprof/dump
cpu() { curl -sf "$C" | awk '$1=="engine_self_us"{a=$2} $1=="engine_cgroup_us"{b=$2} $1=="exec_cgroup_us"{c=($2<0?0:$2)} END{print a, b, c}'; }
t() {
  n=$1; shift; set -- $(cpu) "$@"; a0=$1; b0=$2; c0=$3; shift 3; s=$(ms)
  timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt
  e=$(ms); set -- $(cpu)
  echo "$n wall_ms=$((e-s)) cpu_ms engine=$((($1-a0)/1000)) runtime=$((($2-b0-($1-a0))/1000)) containers=$((($3-c0)/1000)) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-200)" >> /out/summary.txt
}
R="-s call gcexp replay --src ./yq"
P="-s call gcexp plain --src ./yq"
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
t plain-cold $P --volume pc-$NONCE --salt pc-$NONCE --nonce 1
curl -sf "$D?flush=1" -o /dev/null
t first-c16 $R --salt first-$NONCE --concurrency 16 --nonce f
curl -sf "$D?flush=1" -o /out/first-c16.dump
for k in 1 2; do
  curl -sf "$D?flush=1" -o /dev/null
  t cold$k-c16 $R --salt c$k-$NONCE --concurrency 16 --nonce n$k
  curl -sf "$D?flush=1" -o /out/cold$k-c16.dump
done
