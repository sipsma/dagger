# g26: the CPU an exec costs outside its own command. Each session runs N execs of `true`, as
# dagger.io/go runs a package's exec (no init, no Dagger in Dagger), each from its own nonce so none
# is cached. Around every session the engine's CPU counters are read (/debug/g26/cpu): the engine
# process itself, its reaped children (runc, and the `true` it ran) and the engine's whole cgroup.
# Per round: one session of a single exec ("one", the session's own cost, to subtract), one session
# of N execs one after another ("seq"), then P sessions of N/P execs at once ("par"). Each session is timed and gets a wcprof dump, kind-rR.dump, with the exec phases.
# G26_ARGS: "<rounds> <execs per round> <parallel sessions>", e.g. "3 160 16".
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
C=http://dagger-engine:6060/debug/g26/cpu
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
cpu() { curl -sf "$C" -o "/out/$1.cpu" || echo "$1: cpu read failed" >> /out/summary.txt; }
cpudelta() { # $1 = session, $2 = execs: CPU used during it, by kind, in all and per exec
  [ -f /out/$1-0.cpu ] && [ -f /out/$1-1.cpu ] || return 0
  awk -v s=$1 -v n=$2 'NR==FNR{a[$1]=$2; next} {d[$1]=$2-a[$1]} END{e=d["self_user_us"]+d["self_sys_us"]; c=d["children_user_us"]+d["children_sys_us"]; g=d["cgroup_usage_us"]; printf "%s cpu_s engine=%.2f children=%.2f cgroup=%.2f; per exec ms engine=%.2f children=%.2f cgroup=%.2f\n", s, e/1e6, c/1e6, g/1e6, e/1e3/n, c/1e3/n, g/1e3/n}' /out/$1-0.cpu /out/$1-1.cpu >> /out/summary.txt
}
set -- $G26_ARGS
rounds=${1:-3}
n=${2:-160}
par=${3:-16}
IMG=$(dagger -s -c "container | from alpine:3.20 | image-ref" | tail -1)
echo "image $IMG; $n execs per round; $par sessions at once for par" >> /out/summary.txt
script() { # $1 = name, $2 = first index, $3 = count
  for i in $(seq "$2" $(( $2 + $3 - 1 ))); do
    echo "container | from $IMG | with-env-variable G26 $NONCE-$1-$i | with-exec --no-init --disable-dagger-in-dagger -- true | stdout"
  done
}
session() { # $1 = name, $2 = sessions, $3 = execs per session
  for s in $(seq 1 "$2"); do script "$1-s$s" 1 "$3" > "/work/$1-s$s.dsh"; done
  flush; cpu "$1-0"; t0=$(ms)
  for s in $(seq 1 "$2"); do
    (dagger -s -c "$(cat /work/$1-s$s.dsh)" > "/out/$1-s$s.txt" 2>&1 || echo "FAILED rc=$?" >> "/out/$1-s$s.txt") &
  done
  wait
  t1=$(ms); cpu "$1-1"
  echo "$1 wall_ms=$(( t1 - t0 )) execs=$(( $2 * $3 )) :: $(tail -1 "/out/$1-s1.txt" | cut -c1-60)" >> /out/summary.txt
  cpudelta "$1" $(( $2 * $3 ))
  dump "$1"
}
# Untimed: the image and a few execs.
session warm 1 4
for r in $(seq 1 "$rounds"); do
  session one-r$r 1 1
  session seq-r$r 1 "$n"
  session par-r$r "$par" $(( n / par ))
done
