# Engine CPU per exec, with each session's own cost separated out. Each session runs execs of
# `true` (no init, no Dagger in Dagger), each from its own nonce so none is cached. Per round:
#   one   1 session of 1 exec          par1  16 sessions of 1 exec at once
#   seq   1 session of N execs         par   16 sessions of N/16 execs at once
# The engine's CPU (/debug/g26/cpu) is read around every step. Per exec, one after another:
# (seq - one) / (N - 1); 16 sessions at once: (par - par1) / (N - 16).
# G26_ARGS: "<rounds> <execs per round>", e.g. "3 160".
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
C=http://dagger-engine:6060/debug/g26/cpu
cpu() { curl -sf "$C" -o "/out/$1.cpu" || echo "$1: cpu read failed" >> /out/summary.txt; }
cpudelta() {
  awk -v s=$1 -v n=$2 'NR==FNR{a[$1]=$2; next} {d[$1]=$2-a[$1]} END{e=d["self_user_us"]+d["self_sys_us"]; c=d["children_user_us"]+d["children_sys_us"]; g=d["cgroup_usage_us"]; printf "%s cpu_s engine=%.3f children=%.3f cgroup=%.3f execs=%d\n", s, e/1e6, c/1e6, g/1e6, n}' /out/$1-0.cpu /out/$1-1.cpu >> /out/summary.txt
}
set -- $G26_ARGS
rounds=${1:-3}
n=${2:-160}
IMG=$(dagger -s -c "container | from alpine:3.20 | image-ref" | tail -1)
echo "image $IMG; $n execs per round" >> /out/summary.txt
script() { for i in $(seq 1 "$2"); do echo "container | from $IMG | with-env-variable G26 $NONCE-$1-$i | with-exec --no-init --disable-dagger-in-dagger -- true | stdout"; done; }
session() { # $1 name, $2 sessions, $3 execs each
  for s in $(seq 1 "$2"); do script "$1-s$s" "$3" > "/work/$1-s$s.dsh"; done
  cpu "$1-0"; t0=$(ms)
  for s in $(seq 1 "$2"); do (dagger -s -c "$(cat /work/$1-s$s.dsh)" > "/out/$1-s$s.txt" 2>&1 || echo "FAILED rc=$?" >> "/out/$1-s$s.txt") & done
  wait
  t1=$(ms); cpu "$1-1"
  echo "$1 wall_ms=$((t1 - t0)) sessions=$2 execs=$(($2 * $3))" >> /out/summary.txt
  cpudelta "$1" $(($2 * $3))
}
session warm 1 4
for r in $(seq 1 "$rounds"); do
  session one-r$r 1 1
  session par1-r$r 16 1
  session seq-r$r 1 "$n"
  session par-r$r 16 $((n / 16))
done
grep -c FAILED /out/*.txt | grep -v ':0$' >> /out/summary.txt || true
