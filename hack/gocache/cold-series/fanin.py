"""withFiles source-mount fan-in for the gcexp per-package replay.

Each package's /work/lib is one Directory.withFiles call with one source
file per dependency, and each dependency's archive comes from that
package's own exec-output snapshot. So source mounts per first build =
sum of dependency-set sizes over distinct withFiles calls (identical
dependency sets are the same call and evaluate once), and distinct source
snapshots = distinct dependency packages.

Usage: python3 -I fanin.py <file>
  <file> is either a `go build -n` plan (as gcexp replay plans it:
  go build -n -trimpath -buildvcs=false -o /out/bin . with an empty GOCACHE)
  or a gcexp replay output made with --graph (its "DEP pkg: deps" lines).
"""
import collections
import re
import sys

text = open(sys.argv[1]).read()
deps = {}
if re.search(r"^DEP ", text, re.M):
    for line in text.split("\n"):
        if line.startswith("DEP "):
            p, _, rest = line[4:].partition(": ")
            deps[p] = frozenset(rest.split())
    source = "replay --graph output"
else:
    re_mkdir = re.compile(r"^mkdir -p \$WORK/(b\d+)/")
    re_ref = re.compile(r"\$WORK/(b\d+)")
    blocks, cur = {}, None
    for line in text.split("\n"):
        m = re_mkdir.match(line)
        if m:
            cur = blocks.setdefault(m.group(1), set())
        if cur is not None:
            cur.update(re_ref.findall(line))
    deps = {b: frozenset(v - {b}) for b, v in blocks.items()}
    source = "go build -n plan"

calls = {d for d in deps.values() if d}
mounts = sum(len(d) for d in calls)
sources = set().union(*calls)
fan = sorted(collections.Counter(s for d in calls for s in d).values(), reverse=True)
print(f"source: {source}; packages {len(deps)}, with dependencies {sum(1 for d in deps.values() if d)}")
print(f"distinct withFiles calls: {len(calls)}")
print(f"source mounts per first build: {mounts}")
print(f"distinct source snapshots: {len(sources)}; mean fan-in {mounts/len(sources):.1f}, median {fan[len(fan)//2]}")
print(f"fan-in top ten: {fan[:10]}; sources read once: {sum(1 for x in fan if x == 1)}")
print(f"mounts left if each source is mounted once: {len(sources)}; saved {mounts-len(sources)} ({100*(mounts-len(sources))/mounts:.0f}%)")
