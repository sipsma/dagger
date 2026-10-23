# g26: what a shell costs a package's exec. One small package compiles in many execs, three ways:
#   shell  the script dagger.io/go runs: sh makes the directory, writes the import config, compiles;
#   stdin  the compiler as the exec's command, reading the import config from standard input and
#          writing its archive into a mounted directory;
#   file   the compiler as the exec's command, with the import config written into the container
#          first.
# Each round runs N execs of each, one session per way, the ways in turn, so drift spreads over all
# three. Each session is timed and gets a wcprof dump, kind-rR.dump, whose report has the exec phases.
# G26_ARGS: "<rounds> <execs per session>", e.g. "5 20".
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
set -- $G26_ARGS
rounds=${1:-5}
n=${2:-20}
GO=docker.io/library/golang:1.26
base() { printf 'container | from %s | with-new-file /src/p.go "package p; func F(x int) int { return x * 2 }" | with-env-variable G26 %s' "$GO" "$1"; }
# The image is resolved once by digest, so every session's execs start from the same rootfs.
GO=$(dagger -s -c "container | from $GO | image-ref" | tail -1)
# The compiler of the engine's platform, wherever the image keeps it.
TOOL=$(dagger -s -c "container | from $GO | with-exec -- go env GOTOOLDIR | stdout" | tail -1)/compile
echo "image $GO; compiler $TOOL" >> /out/summary.txt
case $TOOL in /*/compile) ;; *) echo "no compiler: $TOOL" >> /out/summary.txt; exit 1 ;; esac
step() { # $1 = way, $2 = round, $3 = execs
  name=$1-r$2
  : > /work/$name.dsh
  for i in $(seq 1 "$3"); do
    nonce="$NONCE-$name-$i"
    case $1 in
      shell) echo "$(base "$nonce") | with-exec -- sh -c 'set -e; mkdir -p /work/p /work/p/; echo \"# import config\" > /work/p/importcfg; $TOOL -c=4 -o /work/p/p.a -p p -importcfg /work/p/importcfg -pack /src/p.go' | directory /work/p | entries" ;;
      stdin) echo "$(base "$nonce") | with-mounted-directory /work/lib \$(directory) | with-exec --stdin \"# import config\" -- $TOOL -c=4 -o /work/lib/p.a -p p -importcfg /dev/stdin -pack /src/p.go | directory /work/lib | entries" ;;
      file)  echo "$(base "$nonce") | with-new-file /work/p/importcfg \"# import config\" | with-exec -- $TOOL -c=4 -o /work/p/p.a -p p -importcfg /work/p/importcfg -pack /src/p.go | directory /work/p | entries" ;;
    esac >> /work/$name.dsh
  done
  flush; s=$(ms)
  dagger -s -c "$(cat /work/$name.dsh)" > "/out/$name.txt" 2>&1 || echo "FAILED rc=$?" >> "/out/$name.txt"
  echo "$name wall_ms=$(( $(ms) - s )) :: $(tail -1 "/out/$name.txt" | cut -c1-60)" >> /out/summary.txt
  dump "$name"
}
# Untimed: the image and one exec of each way.
for w in shell stdin file; do step $w 0 1; done
for r in $(seq 1 "$rounds"); do for w in shell stdin file; do step $w $r "$n"; done; done
