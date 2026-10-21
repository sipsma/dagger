# g24 hybrid spike, in run.sh's client container: /w is hack/gocache/g24/hybrid, /sdk the Go SDK,
# /y yq. Builds the hybrid tool in the Go image, then runs bench.sh there with nested Dagger, and
# copies its results to /out.
export DAGGER_LEAVE_OLD_ENGINE=true  # every dagger call here must leave other engines alone (Erik, 2026-10-13)
set -e
IMG='golang:1.26.6@sha256:0d1d3a794be25f809dd2cb3160d8c73276c4056a9f8242a138e908ddeee7b6b6'
cat > /out/bench.dsh <<EOF
tool=\$(container | from $IMG | with-directory /h \$(host | directory /w/tool) | with-directory /h/sdk \$(host | directory /sdk) | with-workdir /h | with-env-variable CGO_ENABLED 0 | with-exec -- sh -c "go mod tidy && go build -o /hybrid ." | file /hybrid)
container | from $IMG | with-directory /seed \$(host | directory /y) | with-file /usr/local/bin/hybrid \$tool | with-file /bench.sh \$(host | file /w/bench.sh) | with-env-variable NONCE $NONCE | with-env-variable IMG $IMG | with-env-variable ROUNDS $G24_ROUNDS | with-exec -- sh /bench.sh | directory /out | export /out/bench
EOF
dagger -c "$(cat /out/bench.dsh)" > /out/dagger.log 2>&1 || echo "dagger rc=$?" >> /out/dagger.log
cp /out/bench/summary.txt /out/summary.txt 2>/dev/null || { echo "no summary" > /out/summary.txt; tail -50 /out/dagger.log >> /out/summary.txt; }
