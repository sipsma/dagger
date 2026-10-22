# g25: dagger/dagger's -race test binary for ./core/integration (the package the test-base split
# compiles), three ways, on one fresh dev engine. Runs in run.sh's client container; /d is the
# dagger/dagger tree.
#   T, today: dagger/dagger's own toolchain module, .dagger/modules/go of this tree, its Env (Wolfi,
#      its Go cache volumes, source at /app), then go test -c in it.
#   B, new: dagger.io/go at the ref G25_ARGS names, GoPackage.testBinary: one cached exec per
#      package, no cache volumes, mounted at /app, on a Wolfi base with cgo.
#   P, plain: go test -c in one container on B's Wolfi base, a local GOCACHE, timed inside the
#      container with no Dagger call around it.
# Every arm builds with -race -vet=off and one -X stamp: B never vets, so all three compare the
# compile alone. Today's CI runs go test, whose vet is not timed here.
# G25_ARGS: "<order> <B ref>": order "TB" or "BT" (ABBA across series), or "check" (each arm's first
# build only, untimed as a comparison). P always runs after T and B.
# Per arm: preload (T: module load and go mod download into its volume; B: module load, its module
# download and helper tools, through a throwaway test package); then first build, no change, edit D
# (internal/cloud/auth, in the test's dependencies), no change, edit I (a string in one
# core/integration test file), no change. Each build is "test binary | size": built, not run.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
set -- $G25_ARGS
order=${1:?G25_ARGS needs the order}
REF_B=${2:?G25_ARGS needs the dagger.io/go ref}
PKG=${G25_PKG:-core/integration}
STAMP=-ldflags=-X=github.com/dagger/dagger/internal/version/buildinfo.InjectedVCSRevision=g25
WOLFI='container | from cgr.dev/chainguard/wolfi-base | with-exec -- apk add --no-cache go-1.26 build-base git ca-certificates | with-env-variable GOPATH /go | with-env-variable CGO_ENABLED 1'
echo "order $order; B=$REF_B; package ./$PKG; -race -vet=off $STAMP" >> /out/summary.txt
for arm in T B P; do
  mkdir -p /rm/$arm && cp -r /d /rm/$arm/p && rm -f /rm/$arm/p/dagger.lock
  (cd /rm/$arm/p && git init -q && git add -A && git -c gc.auto=0 -c user.email=g25@x -c user.name=g25 commit -qm src)
done
printf '[modules.ci]\nsource = ".dagger/modules/go"\n' > /rm/T/p/dagger.toml
printf '[modules.go]\nsource = "%s"\n' "$REF_B" > /rm/B/p/dagger.toml
t() { arm=$1; n=$2; shift 2; s=$(ms); (cd /rm/$arm/p && timeout 3600 dagger "$@") > /out/$arm-$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$arm-$n.txt; e=$(ms); echo "$arm-$n wall_ms=$((e-s)) :: $(tail -1 /out/$arm-$n.txt | tr '\n' ' ' | cut -c1-100)" >> /out/summary.txt; }
BUILD_T="ci --race --cgo | env | with-exec -- go test -c -race -vet=off $STAMP -o /tmp/integration.test ./$PKG | file /tmp/integration.test | size"
BUILD_B="go --base \$($WOLFI) --mount-path /app --build-flags -race,$STAMP | packages | get $PKG | test-binary | size"
cmd() { case $1 in T) printf '%s' "$BUILD_T" ;; B) printf '%s' "$BUILD_B" ;; esac; }
build() { flush; t $1 $2 -s -c "$(cmd $1)"; dump $1-$2; }
preload() {
  case $1 in
    T) t T preload -s -c 'ci | download | version' ;;
    B)
      mkdir -p /rm/B/p/g25warm && printf 'package g25warm\n\nimport "testing"\n\nfunc TestWarm(t *testing.T) {}\n' > /rm/B/p/g25warm/warm_test.go
      t B preload -s -c "go --base \$($WOLFI) --mount-path /app --build-flags -race,$STAMP | packages | get g25warm | test-binary | size"
      rm -rf /rm/B/p/g25warm ;;
  esac
}
if [ "$order" = check ]; then
  for arm in T B; do preload $arm; t $arm check-build -s -c "$(cmd $arm)"; done
  exit 0
fi
edit() { # $1 = name, $2 = file, $3 = sed expression, $4 = the text it changes
  for arm in T B; do
    if [ "$(grep -c "$4" /rm/$arm/p/$2)" != 1 ]; then echo "$1: expected one match in $2" >> /out/summary.txt; exit 0; fi
    sed -i "$3" /rm/$arm/p/$2
  done
  for arm in $arms; do build $arm $1; t $arm $1-noop -s -c "$(cmd $arm)"; done
}
EDIT_D_FILE=internal/cloud/auth/auth.go
EDIT_D_SED='s/could not acquire lock on %s: timed out/could not acquire the lock on %s: timed out/'
EDIT_D_TEXT='could not acquire lock on %s: timed out'
EDIT_I_FILE=core/integration/workspace_compat_test.go
EDIT_I_SED='s/unexpected getent output: %q/unexpected getent output %q/'
EDIT_I_TEXT='unexpected getent output: %q'
arms=$(echo "$order" | sed 's/./& /g')
for arm in $arms; do preload $arm; done
for arm in $arms; do build $arm first; t $arm noop -s -c "$(cmd $arm)"; done
edit editD "$EDIT_D_FILE" "$EDIT_D_SED" "$EDIT_D_TEXT"
edit editI "$EDIT_I_FILE" "$EDIT_I_SED" "$EDIT_I_TEXT"
# P: a fresh tree and GOCACHE, timed inside one container: go mod download untimed, then the same
# steps and edits. An empty dagger.toml keeps the tree's own modules out of this one call.
: > /rm/P/p/dagger.toml
cat > /rm/plain.sh <<EOF
set -e
ms() { awk '{printf "%d", \$1*1000}' /proc/uptime; }
edit() { if [ "\$(grep -c "\$3" "\$1")" != 1 ]; then echo "edit: expected one match in \$1" >> /out/plain.txt; exit 1; fi; sed -i "\$2" "\$1"; }
mkdir -p /out && cd /app && export GOCACHE=/tmp/gocache && go mod download
t() { n=\$1; s=\$(ms); go test -c -race -vet=off $STAMP -o /tmp/integration.test ./$PKG; e=\$(ms); echo "P-\$n wall_ms=\$((e-s))" >> /out/plain.txt; }
t first; t noop
edit $EDIT_D_FILE '$EDIT_D_SED' '$EDIT_D_TEXT'; t editD; t editD-noop
edit $EDIT_I_FILE '$EDIT_I_SED' '$EDIT_I_TEXT'; t editI; t editI-noop
EOF
(cd /rm/P/p && timeout 7200 dagger -s -c "$WOLFI | with-directory /app \$(host | directory . --exclude .git --exclude dagger.toml) | with-workdir /app | with-file /plain.sh \$(host | file /rm/plain.sh) | with-env-variable NONCE $NONCE | with-exec -- sh /plain.sh | directory /out | export /out/plain") > /out/plain.log 2>&1 || echo "plain: FAILED rc=$?" >> /out/summary.txt
cat /out/plain/plain.txt >> /out/summary.txt 2>/dev/null || echo "plain: no plain.txt" >> /out/summary.txt
