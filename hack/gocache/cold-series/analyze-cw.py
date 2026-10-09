"""Compile-work gap analysis for one inner-cw.sh slot directory.

Usage: python3 -I analyze.py <slot dir with the copied .txt outputs>
"""
import os
import re
import statistics as st
import sys

d = sys.argv[1]


def read(name):
    with open(os.path.join(d, name + ".txt")) as f:
        return f.read()


def replay(name):
    """Per-package (script ms, tool ms), DEP graph, header."""
    text = read(name)
    lines = text.split("\n")
    pk, deps = {}, {}
    for i, l in enumerate(lines):
        m = re.match(r"^[0-9a-f]{12} (\d+)ms (\d+)ms$", l)
        if m and i + 1 < len(lines):
            pk[lines[i + 1].strip()] = (int(m.group(1)), int(m.group(2)))
        if l.startswith("DEP "):
            p, _, rest = l[4:].partition(": ")
            deps[p] = rest.split() if rest else []
    head = "\n".join(lines[:2])
    tot = re.search(r"total ([0-9.]+)(m?s)", head)
    total = float(tot.group(1)) / (1000 if tot.group(2) == "ms" else 1) if tot else None
    return pk, deps, total, head


def plain(name):
    """Per-package build ms (main also gets its link), wall span, total."""
    text = read(name)
    acts, spans = {}, []
    link = {}
    for l in text.split("\n"):
        m = re.match(r"^PACT (build|link) (\S+) ([0-9.]+) ([0-9.]+)$", l)
        if not m:
            continue
        mode, p, s, dur = m.group(1), m.group(2), float(m.group(3)), float(m.group(4))
        spans.append((s, s + dur))
        if mode == "build":
            acts[p] = acts.get(p, 0) + dur
        else:
            link[p] = dur
    for p, dur in link.items():
        acts[p] = acts.get(p, 0) + dur
    tot = re.search(r"total ([0-9.]+)(m?s)", text.split("\n")[0])
    total = float(tot.group(1)) / (1000 if tot.group(2) == "ms" else 1) if tot else None
    first = min(s for s, _ in spans) if spans else 0
    last = max(e for _, e in spans) if spans else 0
    return acts, (last - first) / 1000, total


def critpath(weights, deps):
    memo = {}

    def f(p):
        if p in memo:
            return memo[p]
        best, bestp = 0.0, []
        for q in deps.get(p, []):
            v, path = f(q)
            if v > best:
                best, bestp = v, path
        memo[p] = (best + weights.get(p, 0.0), bestp + [p])
        return memo[p]

    roots = [p for p in deps if not any(p in v for v in deps.values())]
    return max((f(r) for r in roots), key=lambda x: x[0])


r1, deps, r1tot, _ = replay("cold1-r16c4")
print(f"packages {len(r1)}, graph nodes {len(deps)}")
plains = {n: plain(n) for n in ("plain-p12", "plain-c4", "plain-p1")}
replays = {"cold1-r16c4": (r1, r1tot)}
for n in ("cold2-r16c4", "cold3-r16c12", "cold4-r12c4", "cold5-r1c12"):
    pk, _, tot, _ = replay(n)
    replays[n] = (pk, tot)

print("\n== totals (s): wall-ish total, sum of per-package compile work, compile-only critical path")
for n, (acts, span, tot) in plains.items():
    cp, path = critpath({p: v for p, v in acts.items()}, deps)
    print(f"{n:14s} total {tot:7.2f}  actions span {span:6.2f}  sum {sum(acts.values())/1000:7.2f}  critpath {cp/1000:6.2f} ({len(path)} pkgs)")
for n, (pk, tot) in replays.items():
    tools = {p: v[1] for p, v in pk.items()}
    script = {p: v[0] for p, v in pk.items()}
    cpt, patht = critpath(tools, deps)
    cps, _ = critpath(script, deps)
    print(f"{n:14s} total {tot:7.2f}  sum tools {sum(tools.values())/1000:7.2f}  sum script {sum(script.values())/1000:7.2f}  critpath tools {cpt/1000:6.2f} script {cps/1000:6.2f} ({len(patht)} pkgs)")

print("\n== paired per-package compile work, replay / plain (packages in both)")
pairs = [("cold5-r1c12", "plain-p1", "isolated, -c=12"), ("cold2-r16c4", "plain-c4", "loaded, -c=4"),
         ("cold3-r16c12", "plain-p12", "loaded, -c=12"), ("cold2-r16c4", "plain-p12", "as run: replay -c=4 vs plain defaults")]
for rn, pn, label in pairs:
    pk = replays[rn][0]
    acts = plains[pn][0]
    common = [p for p in pk if p in acts and acts[p] > 0]
    ratios = [pk[p][1] / acts[p] for p in common]
    sr = sum(pk[p][1] for p in common) / sum(acts[p] for p in common)
    print(f"{rn} vs {pn} ({label}): n={len(common)} median ratio {st.median(ratios):.2f}, sum ratio {sr:.2f}, "
          f"replay sum {sum(pk[p][1] for p in common)/1000:.2f}s plain sum {sum(acts[p] for p in common)/1000:.2f}s")

print("\n== script overhead outside tool commands (ms per package)")
for n, (pk, _) in replays.items():
    ov = [v[0] - v[1] for v in pk.values()]
    print(f"{n:14s} median {st.median(ov):5.1f}  p90 {sorted(ov)[int(0.9*len(ov))]:5d}  sum {sum(ov)/1000:6.2f}s")

print("\n== compile-only critical path of the as-run replay (cold1-r16c4), per package: replay tool ms vs plain-p12 / plain-p1 ms")
cp, path = critpath({p: v[1] for p, v in r1.items()}, deps)
for p in path:
    print(f"  {p:60s} r16c4 {r1[p][1]:6d}  r1c12 {replays['cold5-r1c12'][0].get(p,(0,0))[1]:6d}  p12 {plains['plain-p12'][0].get(p,0):7.1f}  p1 {plains['plain-p1'][0].get(p,0):7.1f}")
