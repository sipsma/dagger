# Probe the real module's CLI surface on this engine (functional only).
set +e
mkdir -p /rm && cp -r /work/yq /rm/yq && cd /rm/yq && git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm yq
printf '[modules.go]\nsource = "%s"\n' "${GOMOD_REF:-github.com/dagger/go@334136faaa1cd36ac26dfdc648eda907ce99c063}" > /rm/yq/dagger.toml
p() { echo "### $*" >> /out/probe.txt; timeout 900 dagger "$@" >> /out/probe.txt 2>&1; echo "rc=$?" >> /out/probe.txt; }
p -c 'go | packages | keys'
p -c 'go | module . | base | with-directory . $(go | module . | source) | with-exec go,mod,download | stdout'
p -c 'go | packages | get . | binary | size'
p -c 'go | packages | get . | binary | size'
echo "probe done" > /out/summary.txt
