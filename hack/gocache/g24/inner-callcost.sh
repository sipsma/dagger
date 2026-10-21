# g24 step 4: what one uncached call costs, on one fresh dev engine. Runs in run.sh's client
# container, with the callcost module at /work.
# Each case is one "dagger call ... run": n calls of one kind, at most c at once (c = 1 is a serial
# chain), each with a fresh salt so every call is a cache miss. The function times each call from
# the caller's side and prints one line; summary.txt gets that line plus the CLI's wall time.
# Kinds: selfcall (a Go module self-call returning a string), selfcall-args (the same with eight
# arguments), selfcall-file (a self-call returning a file it wrote, via currentModule.workdirFile),
# exec (a trivial withExec, nesting off), exec-nest (the same, nesting on), exec-file (the
# per-package module's pattern: an exec writing a file, then sync the directory, then the file).
# Untimed preload first (module load and codegen, the alpine image, one call of each kind). Then
# G24_ROUNDS rounds (default 3) of all cases, the case order rotated each round. Round 1 also
# writes one wcprof dump per case, for the phase split. G24_N_CHAIN and G24_N_FAN (defaults 60 and 320)
# set the call counts; OUT (default /out) is the output directory.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
OUT=${OUT:-/out}; NC=${G24_N_CHAIN:-60}; NF=${G24_N_FAN:-320}
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "$OUT/$1.dump" || echo "$1: dump failed" >> $OUT/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
t() { # $1 = name, then kind n c size
  name=$1; s=$(ms)
  timeout 900 dagger call -m . run --kind "$2" --n "$3" --c "$4" --size "$5" --salt "$NONCE-$name" > $OUT/$name.txt 2>&1 || echo "FAILED rc=$?" >> $OUT/$name.txt
  e=$(ms); echo "$name cli_wall_ms=$((e-s)) :: $(grep -o 'kind=.*mean_ms=[0-9.]*' $OUT/$name.txt | tail -1)" >> $OUT/summary.txt
}
echo "rounds ${G24_ROUNDS:-3}; chain $NC; fan $NF; nonce $NONCE" >> $OUT/summary.txt
for k in selfcall selfcall-args selfcall-file exec exec-nest exec-file; do t pre-$k $k 2 1 1024; done
cases="chain-selfcall:selfcall:$NC:1:0
chain-selfcall-args:selfcall-args:$NC:1:0
chain-selfcall-file-1k:selfcall-file:$NC:1:1024
chain-selfcall-file-1m:selfcall-file:$NC:1:1048576
chain-exec:exec:$NC:1:0
chain-exec-nest:exec-nest:$NC:1:0
chain-exec-file-1k:exec-file:$NC:1:1024
chain-exec-file-1m:exec-file:$NC:1:1048576
fan-selfcall:selfcall:$NF:16:0
fan-selfcall-args:selfcall-args:$NF:16:0
fan-selfcall-file-1k:selfcall-file:$NF:16:1024
fan-selfcall-file-1m:selfcall-file:$NF:16:1048576
fan-exec:exec:$NF:16:0
fan-exec-nest:exec-nest:$NF:16:0
fan-exec-file-1k:exec-file:$NF:16:1024
fan-exec-file-1m:exec-file:$NF:16:1048576"
total=$(echo "$cases" | wc -l)
r=1
while [ $r -le ${G24_ROUNDS:-3} ]; do
  shift_by=$(( (r - 1) * 5 % total ))
  echo "$cases" | awk -v k=$shift_by -v n=$total '{ a[NR] = $0 } END { for (i = 0; i < n; i++) print a[(i + k) % n + 1] }' > $OUT/.order
  while IFS=: read -r cname kind n c size; do
    flush
    t r$r-$cname $kind $n $c $size
    if [ $r = 1 ]; then dump r1-$cname; fi
  done < $OUT/.order
  r=$((r + 1))
done
