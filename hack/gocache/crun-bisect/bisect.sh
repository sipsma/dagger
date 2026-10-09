# crun per-start cost bisect (runs privileged in a container; /crun = the engine's crun, /spec = engine spec).
# Variants: base (crun on a writable fs, so it re-execs via memfd), ro (crun on a read-only fs: no re-exec),
# nomask (no masked/readonly paths), nocg (--cgroup-manager=disabled). Per variant: 40 serial run+delete,
# then 160 run+delete with 16 concurrent workers; two rounds in reversed order.
set -e
apk add --no-cache jq coreutils strace >/dev/null
ns() { date +%s%N; }
mkdir -p /w /ro && cp /crun /w/crun && chmod +x /w/crun
mount -t tmpfs tmpfs /ro && cp /crun /ro/crun && chmod +x /ro/crun && mount -o remount,ro /ro
/w/crun --version | head -1
base='.root.path="/b/rootfs" | .process.args=["/bin/true"] | .process.env=["PATH=/bin:/usr/bin"] | .process.cwd="/" | .linux.namespaces |= map(if .type=="network" then {type:"network"} else . end) | .linux.cgroupsPath="/g18b/x"'
mk() { mkdir -p /v/$1; jq "$base | $2" /spec/engine.json > /v/$1/config.json; ln -sfn /b/rootfs /v/$1/rootfs; }
mk full '.'
mk nomask 'del(.linux.maskedPaths) | del(.linux.readonlyPaths)'
# variant: name crunpath bundle extra-global-flags
var() { case $1 in base) echo "/w/crun full";; ro) echo "/ro/crun full";; nomask) echo "/w/crun nomask";; nocg) echo "/w/crun full --cgroup-manager=disabled";; esac; }
one() { rt=$1; b=$2; fl=$3; id=$4; $rt $fl run --keep -b /v/$b $id >/dev/null; $rt $fl delete $id; }
serial() { set -- $(var $1); rt=$1; b=$2; fl=${3:-}; s=0; for i in $(seq 40); do t0=$(ns); one $rt $b "$fl" s$i-$$; t1=$(ns); s=$((s+t1-t0)); done; echo "$s"; }
par() { set -- $(var $1); rt=$1; b=$2; fl=${3:-}; t0=$(ns); for w in $(seq 16); do ( for i in $(seq 10); do one $rt $b "$fl" p$w-$i-$$; done ) & done; wait; t1=$(ns); echo $((t1-t0)); }
for round in 1 2; do
  if [ $round = 1 ]; then order="base ro nomask nocg"; else order="nocg nomask ro base"; fi
  for v in $order; do
    s=$(serial $v); p=$(par $v)
    echo "round$round $v serial_run+delete_us=$((s/40000)) par16_160_total_ms=$((p/1000000)) par16_per_start_us=$((p/160000))"
  done
done
echo "--- one straced base start (clone3 result, execve count, subtree_control writes):"
strace -f -o /tmp/st /w/crun run --keep -b /v/full st1 >/dev/null 2>&1 || true; /w/crun delete st1 || true
grep -E 'clone3\(' /tmp/st | sed 's/stack=.*}/.../' | cut -c1-200 | head -3
echo "execve: $(grep -c 'execve(' /tmp/st)  subtree_control writes: $(grep -c 'subtree_control' /tmp/st)  memfd_create: $(grep -c memfd_create /tmp/st)  total syscalls: $(wc -l < /tmp/st)"
strace -f -o /tmp/st2 /ro/crun run --keep -b /v/full st2 >/dev/null 2>&1 || true; /ro/crun delete st2 || true
echo "ro: execve: $(grep -c 'execve(' /tmp/st2)  memfd_create: $(grep -c memfd_create /tmp/st2)  total syscalls: $(wc -l < /tmp/st2)"
