# Runs inside the client container (from-source CLI, bound to the dev engine as dagger-engine).
# Functional smoke of the engine's container runtime: a plain exec, a nested `dagger` call, an env
# secret, a service, and an emulated exec. One line per check in /out/summary.txt; a check passes
# when its stdout (the call's result, not the progress output) contains the expected line.
set -e
export SMOKE_SECRET=s3cret-$NONCE
t() { n=$1; want=$2; shift 2; timeout 600 dagger -c "$*" > /out/$n.txt 2> /out/$n.err || echo "FAILED rc=$?" >> /out/$n.err; if grep -qx -- "$want" /out/$n.txt; then r=ok; else r=FAIL; fi; echo "$n $r :: $(tr '\n' ' ' < /out/$n.txt | cut -c1-120) $(grep -m1 FAILED /out/$n.err || true)" >> /out/summary.txt; }
t plain-exec "hello-$NONCE" "container | from alpine:3.20 | with-exec -- sh -c 'echo hello-\$0' $NONCE | stdout"
t nested-dagger "nested-$NONCE" "container | from alpine:3.20 | with-exec -- dagger -c 'container | from alpine:3.20 | with-exec -- sh -c \"echo nested-\\\$0\" $NONCE | stdout' | stdout"
t env-secret "secret-ok" "container | from alpine:3.20 | with-secret-variable S \$(secret env://SMOKE_SECRET) | with-exec -- sh -c 'test \"\$S\" = \"$SMOKE_SECRET\" && echo secret-ok' | stdout"
t service "served-$NONCE" "container | from alpine:3.20 | with-service-binding web \$(container | from alpine:3.20 | with-exec -- apk add --no-cache busybox-extras | with-new-file /www/index.html served-$NONCE | with-exposed-port 8080 | as-service --args httpd,-f,-p,8080,-h,/www) | with-exec -- wget -qO- http://web:8080 | stdout"
t emulated-amd64 "x86_64" "container --platform linux/amd64 | from alpine:3.20 | with-exec -- uname -m | stdout"
