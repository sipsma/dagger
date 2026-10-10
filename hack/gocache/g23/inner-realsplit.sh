# E0b: where today's real Go module (github.com/dagger/go main) spends its ~2.65 s per call on yq.
# Runs in the client container; /work is the gcexp workspace with yq at ./yq. After warming the
# module and one full build, every step below is a no-change call, repeated three times:
#   field     go | build-flags                     module load + CLI type loading, no Workspace
#   fieldq    { go { buildFlags } } via dagger api query (no shell type loading of the call chain)
#   keys      go | packages | keys                  + package discovery (searches, globs, scan)
#   get       go | packages | get .                 + the collection lookup
#   binary    go | packages | get . | binary | size + the build call (cached exec)
# wcprof dumps for the third repetition of each.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
GOMOD=${GOMOD_REF:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}
mkdir -p /rm && cp -r /work/yq /rm/yq
(cd /rm/yq && git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm yq)
printf '[modules.go]\nsource = "%s"\n' "$GOMOD" > /rm/yq/dagger.toml
cd /rm/yq
t() { n=$1; shift; s=$(ms); timeout 1500 "$@" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-80)" >> /out/summary.txt; }
t warm dagger -s -c 'go | packages | get . | binary | size'
for i in 1 2 3; do
  [ $i = 3 ] && flush
  t field-$i dagger -s -c 'go | build-flags'
  [ $i = 3 ] && dump field-3
  t fieldq-$i sh -c 'echo "{ go { buildFlags } }" | dagger -s api query'
  [ $i = 3 ] && dump fieldq-3
  t keys-$i dagger -s -c 'go | packages | keys'
  [ $i = 3 ] && dump keys-3
  t get-$i dagger -s -c 'go | packages | get . | path'
  [ $i = 3 ] && dump get-3
  t binary-$i dagger -s -c 'go | packages | get . | binary | size'
  [ $i = 3 ] && dump binary-3
done
