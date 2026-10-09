#!/bin/bash
# Per-process cost of the engine's injected /.init, outside runc: 50x /bin/true run directly, under a slim
# copy of the init (hack/gocache/g18/slim-init: same logic, no session-attachable imports), and under the
# real dagger-init from the dev engine built from this checkout. ROUNDS rounds (default 6) in rotating order.
# Run from the root of a dagger/dagger checkout: hack/gocache/g18/init-cost.sh
set -euo pipefail
dagger -c "slim=\$(container | from golang:1.26 | with-env-variable CGO_ENABLED 0 | with-directory /src \$(directory | with-file go.mod \$(host | file go.mod) | with-file go.sum \$(host | file go.sum) | with-directory slim \$(host | directory hack/gocache/g18/slim-init)) | with-workdir /src | with-exec -- go build -o /slim-init ./slim | file /slim-init)
engine-dev | container | with-file /slim-init \$slim | with-env-variable ROUNDS ${ROUNDS:-6} | with-env-variable N $(date +%s%N) | with-exec -- sh -c 'ln -sf /usr/local/bin/dagger-init /.init; nproc; ls -la /usr/local/bin/dagger-init /slim-init; set -- /bin/true \"/slim-init /bin/true\" \"/.init /bin/true\"; for r in \$(seq \$ROUNDS); do for b in \"\$@\"; do s=\$(date +%s%N); for i in \$(seq 50); do \$b; done; e=\$(date +%s%N); echo \"round \$r \$b: \$(( (e-s)/50000 ))us per run\"; done; set -- \"\$2\" \"\$3\" \"\$1\"; done' | stdout"
