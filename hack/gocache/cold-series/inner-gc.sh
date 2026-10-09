# Engine GC tuning comparison (runs in the client container; /work = gcexp workspace with yq at ./yq).
# Needs an engine with the g18 attribution experiment (/debug/cpu with memory lines). After the preloads:
# a cold plain go build, then two measured cold replays at c16. Each step records the engine's CPU deltas
# (engine process, runtime processes, containers) and, after it, the engine cgroup's memory (current, and
# the peak since the engine started) and the Go heap in use and GC count.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
C=http://dagger-engine:6060/debug/cpu
D=http://dagger-engine:6060/debug/wcprof/dump
cpu() { curl -sf "$C" | awk '$1=="engine_self_us"{a=$2} $1=="engine_cgroup_us"{b=$2} $1=="exec_cgroup_us"{c=($2<0?0:$2)} END{print a, b, c}'; }
mem() { curl -sf "$C" | awk '$1=="engine_memory_current_bytes"{c=$2} $1=="engine_memory_peak_bytes"{p=$2} $1=="go_heap_inuse_bytes"{h=$2} $1=="go_num_gc"{g=$2} $1=="go_gc_cpu_fraction"{f=$2} END{printf "mem_mb current=%d peak=%d heap_inuse=%d num_gc=%d gc_cpu_fraction=%s", c/1048576, p/1048576, h/1048576, g, f}'; }
t() {
  n=$1; shift; set -- $(cpu) "$@"; a0=$1; b0=$2; c0=$3; shift 3; s=$(ms)
  timeout 1500 dagger "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt
  e=$(ms); set -- $(cpu)
  echo "$n wall_ms=$((e-s)) cpu_ms engine=$((($1-a0)/1000)) runtime=$((($2-b0-($1-a0))/1000)) containers=$((($3-c0)/1000)) $(mem) :: $(head -2 /out/$n.txt | tr '\n' ' ' | cut -c1-160)" >> /out/summary.txt
}
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
curl -sf "$C" | grep '^go_' | sed 's/^/engine-gc-config /' >> /out/summary.txt
R="-s call gcexp replay --src ./yq"
P="-s call gcexp plain --src ./yq"
t preload $P --volume "" --salt pre-$NONCE --nonce p
t preload-pack -s call gcexp pack-tool
t plain-cold $P --volume pc-$NONCE --salt pc-$NONCE --nonce 1
for k in 1 2; do
  flush
  t cold$k-c16 $R --salt c$k-$NONCE --concurrency 16 --nonce n$k
done
