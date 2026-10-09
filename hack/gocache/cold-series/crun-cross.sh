#!/bin/bash
# Build crun the way a release builds the arm64 engine's: in an amd64 build container (emulated on an
# arm64 host) cross-compiling with xx for linux/arm64, with the shell steps taken verbatim from
# engine-dev's builder and its pinned versions. Then run that binary natively on this host
# (`crun --version`, `crun features`). Run from the root of a dagger/dagger checkout:
#   hack/gocache/cold-series/crun-cross.sh <outdir>      -> <outdir>/crun, <outdir>/crun-cross.txt
set -euo pipefail
out=$1; mkdir -p "$out"
b=.dagger/modules/engine-dev/build/builder.go; c=.dagger/modules/engine-dev/consts/consts.go; d=engine/distconsts/consts.go
awk '/"sh", "-ec", `/{f=1;next} f&&/^`}\)/{exit} f' "$b" > "$out/build-crun.sh"
v() { sed -n "s/^[[:space:]]*$1[[:space:]]*= \"\(.*\)\"/\1/p" "$2" | head -1; }
crun=$(v CrunVersion $c); crunsum=$(v CrunChecksum $c); jsonc=$(v JSONCVersion $c); jsoncsum=$(v JSONCChecksum $c)
alpine=alpine:$(v AlpineVersion $d); xx=$(v XxImage $c)
apks=$(awk '/"apk", "add", "--no-cache",/{f=1;next} f{print; if(/}\)/) exit}' "$b" | tr -d '\t"{}).' | tr ',' ' ' | xargs)
xxapks=$(awk '/"xx-apk", "add", "--no-cache",/{f=1;next} f{print; if(/}\)/) exit}' "$b" | tr -d '\t"{}).' | tr ',' ' ' | xargs)
echo "crun $crun json-c $jsonc $alpine $xx apk[$apks] xx-apk[$xxapks]" | tee "$out/crun-cross.txt"
dagger -c "container --platform linux/amd64 | from $alpine | with-env-variable BUILDPLATFORM linux/amd64 | with-env-variable TARGETPLATFORM linux/arm64 | with-exec -- apk add --no-cache $apks | with-directory / \$(container --platform linux/amd64 | from $xx | rootfs) | with-exec -- xx-apk add --no-cache $xxapks | with-file /crun.tar.gz \$(http https://github.com/containers/crun/releases/download/$crun/crun-$crun.tar.gz --checksum $crunsum) | with-file /json-c.tar.gz \$(http https://github.com/json-c/json-c/archive/refs/tags/json-c-$jsonc.tar.gz --checksum $jsoncsum) | with-file /build-crun.sh \$(host | file $out/build-crun.sh) | with-exec -- sh -e /build-crun.sh | file /src/crun | export $out/crun"
for args in --version features; do
  echo "## crun $args (native $(uname -m))" >> "$out/crun-cross.txt"
  dagger -c "container | from $alpine | with-file /crun \$(host | file $out/crun) | with-exec -- /crun $args | stdout" >> "$out/crun-cross.txt" 2>"$out/crun-cross-$args.err" || echo "FAILED rc=$?" >> "$out/crun-cross.txt"
done
cat "$out/crun-cross.txt"
