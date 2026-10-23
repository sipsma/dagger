# wcp: one first build of dagger's CLI (./cmd/dagger) with one dagger.io/go ref, on run.sh's fresh
# dev engine. Runs in run.sh's client container: /d is the dagger/dagger tree.
# WCP_ARGS: "<arm> <dagger.io/go ref> [trace]".
# As g26's inner.sh: the tree is copied with a one-file package naming the arm; an untimed warm-up
# builds a throwaway main package with -trimpath added (the module download and the helper tools,
# no compile shared with the timed build); then the timed first build, with the engine's CPU
# counters around it and a wcprof dump of it. With "trace", the ref is a measurement build of the
# old (tiered) dagger.io/go: afterwards a cached query reads each tier's per-package trace out of
# the build's output into /out/first.tiertrace/.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
C=http://dagger-engine:6060/debug/g26/cpu
cpu() { curl -sf "$C" -o "/out/$1.cpu" || true; }
cpudelta() { # $1 = step: CPU seconds used during it, by kind
  [ -f /out/$1-0.cpu ] && [ -f /out/$1-1.cpu ] || return 0
  awk -v s=$1 'NR==FNR{a[$1]=$2; next} {d[$1]=$2-a[$1]} END{printf "%s cpu_s engine=%.2f children=%.2f cgroup=%.2f\n", s, (d["self_user_us"]+d["self_sys_us"])/1e6, (d["children_user_us"]+d["children_sys_us"])/1e6, d["cgroup_usage_us"]/1e6}' /out/$1-0.cpu /out/$1-1.cpu >> /out/summary.txt
}
set -- $WCP_ARGS
arm=${1:?WCP_ARGS needs the arm}
ref=${2:?WCP_ARGS needs the ref}
trace=${3:-}
echo "arm $arm; ref $ref; $trace" >> /out/summary.txt
p=/rm/p
mkdir -p $p && cp -r /d/. $p && rm -f $p/dagger.toml $p/dagger.lock
mkdir -p $p/g26arm && printf 'package g26arm\n\nconst Arm = "%s"\n' "$arm" > $p/g26arm/arm.go
printf '[modules.go]\nsource = "%s"\n' "$ref" > $p/dagger.toml
(cd $p && git init -q && git add -A && git -c gc.auto=0 -c user.email=wcp@x -c user.name=wcp commit -qm src)
t() { n=$1; shift; s=$(ms); (cd $p && timeout 3600 dagger "$@") > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
mkdir -p $p/g26warm && printf 'package main\n\nfunc main() {}\n' > $p/g26warm/main.go
t warm -s -c "go --build-flags -trimpath | packages | get g26warm | binary | size"
rm -rf $p/g26warm
flush
cpu first-0
t first -s -c "go | packages | get cmd/dagger | binary | size"
cpu first-1; cpudelta first; dump first
if [ "$trace" = trace ]; then
  t tiertrace -s -c "go | packages | subset --keys cmd/dagger | batch | binary | directory .gobuild-tier-trace | export /out/first.tiertrace"
  flush
fi
