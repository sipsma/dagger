# Functional check (not timing): inside one loaded gomod candidate, does the public Gomod.scan and the
# in-process GoMod.scanned (reached through GoMod.scanError) run the same scanner exec? A probe module
# depends on the candidate's gomod (G23_ARGS: its module ref, e.g.
# github.com/sipsma/dagger/hack/gocache/g23/gomod-proto-d/gomod@<sha>), scans a tiny workspace both
# ways and compares the results. Each probe call writes a fresh nonce into a .go file, so its scans
# are new; after a warm-up call that builds the scanner helper, one shared exec gives
# exec.processRun=1 per call, two separate execs give 2. Also the reverse order, and the test,
# generate and test+generate modes. A wcprof dump per call; run.sh prints the counts.
set -e
ms() { awk '{printf "%d", $1*1000}' /proc/uptime; }
D=http://dagger-engine:6060/debug/wcprof/dump
dump() { curl -sf "$D?flush=1" -o "/out/$1.dump" || echo "$1: dump failed" >> /out/summary.txt; }
flush() { curl -sf "$D?flush=1" -o /dev/null || true; }
REF=${G23_ARGS:?G23_ARGS needs the gomod module ref}
echo "gomod $REF" >> /out/summary.txt
mkdir -p /rm/p/.probe && cd /rm/p
printf 'name = "scanparity"\nengineVersion = "v1.0.0-beta.15"\n\n[runtime]\n  source = "dang"\n\n[[dependencies]]\n  name = "gomod"\n  source = "%s"\n' "$REF" > .probe/dagger-module.toml
cat > .probe/main.dang <<'DANG'
"""
Scans one tiny workspace through gomod's public scan and its in-process one.
"""
type Scanparity {
  let workspace(nonce: String!): Workspace! {
    directory
      .withNewFile("go.mod", "module example.com/scanparity\n\ngo 1.26.1\n")
      .withNewFile("p.go", "package scanparity\n\n// " + nonce + "\n")
      .asWorkspace
  }

  """
  The public scan's error file, then the in-process scan's, or the reverse.
  """
  check(nonce: String!, test: Boolean! = false, generate: Boolean! = false, reverse: Boolean! = false): String! {
    let ws = workspace(nonce)
    let first = if (reverse) {
      gomod.at(".").scanError(ws, test: test, generate: generate) ?? ""
    } else {
      gomod.scan(ws, test: test, generate: generate).file("_root_.err").contents.trimSpace
    }
    let second = if (reverse) {
      gomod.scan(ws, test: test, generate: generate).file("_root_.err").contents.trimSpace
    } else {
      gomod.at(".").scanError(ws, test: test, generate: generate) ?? ""
    }
    if (first != second) { raise "scanner results differ: [" + first + "] [" + second + "]" }
    "ok [" + first + "]"
  }
}
DANG
printf '[modules.scanparity]\nsource = "./.probe"\n' > dagger.toml
git init -q && git add -A && git -c user.email=g23@x -c user.name=g23 commit -qm probe
t() { n=$1; shift; s=$(ms); timeout 900 dagger -s -c "scanparity | check $*" > /out/$n.txt 2>&1 || echo "FAILED rc=$?" >> /out/$n.txt; e=$(ms); echo "$n wall_ms=$((e-s)) :: $(tail -1 /out/$n.txt | cut -c1-120)" >> /out/summary.txt; }
t warm --nonce w$NONCE
flush; t plain --nonce a$NONCE; dump plain
flush; t reverse --nonce b$NONCE --reverse; dump reverse
flush; t test --nonce c$NONCE --test; dump test
flush; t generate --nonce d$NONCE --generate; dump generate
flush; t both --nonce e$NONCE --test --generate; dump both
