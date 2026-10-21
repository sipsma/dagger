# g23 item 4: today's real Go module (A, github.com/dagger/go main) against the new module (B:
# hack/gocache/g23/gomod-pp of this harness's commit, a copy of dagger/go branch g23-perpackage at 50be5b5,
# which builds every binary per package with no fallback),
# on one project, on one fresh dev engine. Runs in the client container; /work/yq is yq and /d is
# run.sh's DSRC (a dagger/dagger source tree, for the dagger project).
# G23_ARGS: "<order> <B ref> <project> [<tiers>]": order "AB" or "BA" (ABBA across series), or
# "check" (only checks both arms work on this engine: B's prepare and one build, A's discovery), project
# "yq" (builds ., edit A in pkg/yqlib) or "dagger" (builds ./cmd/dagger, edit D in
# internal/cloud/auth, then edit T in internal/cmd/dagger), tiers B's perPackageTiers (default
# outside).
# Per arm: preload (A: module load, discovery and go mod download into its go-mod volume; B: the
# same discovery, plus its own module download and helper tools via perPackagePrepare); then first
# build, no change, each edit, no change again. Each build is "binary | size": built, not run.
# Each arm is a different module, so it has its own cache volumes and its first build is cold.
# wcprof dumps for every first build and edit.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
REF_A=${GOMOD_REF_A:-github.com/dagger/go@8a1d6ad09c909a541e7e5fa319bddb5dba83d20b}
set -- $G23_ARGS
order=${1:?G23_ARGS needs the order}
REF_B=${2:?G23_ARGS needs the B module ref}
project=${3:?G23_ARGS needs the project}
tiers=${4:-outside}
case $project in
  yq) src=/work/yq; pkg=. ;;
  dagger) src=/d; pkg=cmd/dagger ;;
  *) echo "unknown project $project" >> /out/summary.txt; exit 0 ;;
esac
echo "order $order; project $project; tiers $tiers; A=$REF_A; B=$REF_B" >> /out/summary.txt
settings() { # $1 = arm, $2 = extra B settings
  d=/rm/$1/p; eval ref=\$REF_$1
  printf '[modules.go]\nsource = "%s"\n' "$ref" > $d/dagger.toml
  [ $1 = B ] && printf '\n[modules.go.settings]\nperPackageTiers = "%s"\n%b' "$tiers" "${2:-}" >> $d/dagger.toml
  true
}
for arm in A B; do
  mkdir -p /rm/$arm && cp -r $src /rm/$arm/p && rm -f /rm/$arm/p/dagger.toml /rm/$arm/p/dagger.lock
  (cd /rm/$arm/p && git init -q && git add -A && git -c gc.auto=0 -c user.email=g23@x -c user.name=g23 commit -qm src)
  settings $arm
done
t() { arm=$1; n=$2; shift 2; s=$(ms); (cd /rm/$arm/p && timeout 2400 dagger "$@") > /out/$arm-$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$arm-$n.txt; e=$(ms); echo "$arm-$n wall_ms=$((e-s)) :: $(tail -1 /out/$arm-$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
BUILD="go | packages | get $pkg | binary | size"
preload() {
  if [ $1 = B ]; then settings B 'perPackagePrepare = true\n'; t B prepare -s -c "$BUILD"; settings B; fi
  t $1 preload -s -c 'go | packages | keys'
  [ $1 = A ] && t A preload-mod -s -c 'go | module . | base | with-directory . $(go | module . | source) | with-exec go,mod,download | sync'
  true
}
build() { flush; t $1 $2 -s -c "$BUILD"; dump $1-$2; }
# "check" only checks both arms work on this engine: B's prepare-only build, one B build, and
# A's discovery. Its times are not a comparison.
if [ "$order" = check ]; then settings B 'perPackagePrepare = true\n'; t B prepare -s -c "$BUILD"; settings B; t B check-build -s -c "$BUILD"; t A preload -s -c 'go | packages | keys'; exit 0; fi
edit() { # $1 = name, $2 = file, $3 = sed expression, $4 = the text it changes
  for arm in A B; do
    if [ "$(grep -c "$4" /rm/$arm/p/$2)" != 1 ]; then echo "$1: expected one match in $2" >> /out/summary.txt; exit 0; fi
    sed -i "$3" /rm/$arm/p/$2
  done
  for arm in $arms; do build $arm $1; t $arm $1-noop -s -c "$BUILD"; done
}
arms=$(echo "$order" | sed 's/./& /g')
for arm in $arms; do preload $arm; done
for arm in $arms; do build $arm first; t $arm noop -s -c "$BUILD"; done
case $project in
  yq) edit editA pkg/yqlib/operator_add.go 's/unable to parse duration \[%v\]: %w/unable to parse the duration [%v]: %w/' 'unable to parse duration \[%v\]: %w' ;;
  dagger)
    edit editD internal/cloud/auth/auth.go 's/could not acquire lock on %s: timed out/could not acquire the lock on %s: timed out/' 'could not acquire lock on %s: timed out'
    edit editT internal/cmd/dagger/artifacts.go 's/addresses select different workspaces: %s and %s/addresses select two different workspaces: %s and %s/' 'addresses select different workspaces: %s and %s' ;;
esac
# Untimed, after everything timed (the folded first-build trace): B once more, cold (a fresh salt;
# downloads and helper tools stay cached) with its per-package timeline ("TL" lines in
# B-timeline.txt), then a plain go build of the same tree with the go command's action graph
# (plaingraph.json), in the module's Go image. Neither is a timing comparison.
settings B "perPackageTimeline = true\nperPackageSalt = \"tl-$NONCE\"\n"
(cd /rm/B/p && timeout 2400 dagger --progress=plain -c "$BUILD") > /out/B-timeline.txt 2>&1 || echo "timeline: FAILED rc=$?" >> /out/summary.txt
echo "timeline: $(grep -c 'TL {' /out/B-timeline.txt) TL lines" >> /out/summary.txt
settings B
v=$(cd /rm/A/p && dagger -s -c 'go | module . | version' 2>/dev/null | tail -1)
(cd /rm/A/p && timeout 2400 dagger -s -c "container | from golang:$v-alpine | with-env-variable GOCACHE /tmp/gocache | with-directory /src \$(host | directory . --exclude .git --exclude dagger.toml) | with-workdir /src | with-exec -- sh -c 'go mod download && mkdir -p /out && go build -debug-actiongraph=/out/graph.json -o /out/bin ./$pkg' | file /out/graph.json | export /out/plaingraph.json") > /out/plaingraph.log 2>&1 || echo "plaingraph: FAILED rc=$?" >> /out/summary.txt
echo "plaingraph: $(wc -c < /out/plaingraph.json 2>/dev/null || echo 0) bytes (go $v)" >> /out/summary.txt
