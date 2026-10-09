#!/bin/bash
# mkrun.sh <variant> <inner-sh-file> <outdir>: prints a dagger shell script that starts a dev engine
# built from the current directory's dagger checkout as a service, with engine state kept per variant
# (cache volume g14-mount-<variant>), and runs the inner script in a client container.
# /work = $BENCH/expmod with $BENCH/yq copied to /work/yq. Outputs in /out -> <outdir>.
variant=$1; inner=$(cat "$2"); outdir=$3; bench=${BENCH:-$HOME/gocache-bench}
inner=${inner//\'/\'\"\'\"\'}
cat <<DSH
dev=\$(engine-dev | increment-subnet)
cidr=\$(\$dev | network-cidr)
svc=\$(\$dev | container | with-exposed-port 1234 | with-mounted-cache /var/lib/dagger \$(cache-volume g14-mount-$variant) | as-service --args="--addr","tcp://0.0.0.0:1234","--network-name","dagger-lab","--network-cidr","\$cidr" --use-entrypoint --insecure-root-capabilities)
engine-dev | install-client --client \$(container | from alpine:3.20 | with-exec -- apk add --no-cache curl bc bash findutils coreutils) --service \$svc | with-mounted-directory /w \$(host | directory $bench/expmod) | with-mounted-directory /wyq \$(host | directory $bench/yq) | with-env-variable RUNID $variant-$(date +%s%N) | with-exec -- bash -c 'set -e; unset DAGGER_SESSION_PORT DAGGER_SESSION_TOKEN; mkdir -p /out; cp -r /w /work; cp -r /wyq /work/yq; cd /work; $inner' | directory /out | export $outdir
DSH
