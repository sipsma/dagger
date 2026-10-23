# One arm per engine: dagger.io/go at one ref builds one project on run.sh's fresh dev engine,
# so nothing one arm builds is in the cache of the other. Runs in run.sh's client container: /y is
# yq v4.49.2, /d the dagger/dagger tree.
# G26_ARGS: "<ref> <project>", project yq (builds .) or dagger (builds ./cmd/dagger).
# Untimed: a warm-up that builds a throwaway main package with -trimpath added, which runs the module
# download and the helper tools and shares no compile with the timed build. Timed: the first build,
# "binary | size": built, not run. A wcprof dump of the first build.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
set -- $G26_ARGS
REF=${1:?G26_ARGS needs the ref}
project=${2:?G26_ARGS needs the project}
case $project in
  yq) src=/y; pkg=. ;;
  dagger) src=/d; pkg=cmd/dagger ;;
  *) echo "unknown project $project" >> /out/summary.txt; exit 0 ;;
esac
echo "project $project; ref $REF" >> /out/summary.txt
mkdir -p /rm/p && cp -r $src/. /rm/p && rm -f /rm/p/dagger.toml /rm/p/dagger.lock
printf '[modules.go]\nsource = "%s"\n' "$REF" > /rm/p/dagger.toml
(cd /rm/p && git init -q && git add -A && git -c gc.auto=0 -c user.email=g27@x -c user.name=g27 commit -qm src)
t() { n=$1; shift; s=$(ms); (cd /rm/p && timeout 3600 dagger "$@") > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
mkdir -p /rm/p/g27warm && printf 'package main\n\nfunc main() {}\n' > /rm/p/g27warm/main.go
t warm -s -c "go --build-flags -trimpath | packages | get g27warm | binary | size"
rm -rf /rm/p/g27warm
curl -sf "$D?flush=1" -o /dev/null || true
t first -s -c "go | packages | get $pkg | binary | size"
curl -sf "$D?flush=1" -o /out/first.dump || echo "first: dump failed" >> /out/summary.txt
