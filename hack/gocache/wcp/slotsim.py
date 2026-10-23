#!/usr/bin/env python3
# wcp: replay a per-package dagger.io/go build through gobuild's exec-slot gate under other
# priorities or slot counts, from one measured build of today's (per-package) build.
#
#   slotsim.py <walk.json> <graph.json> [--src DIR] [--slots N ...]
#
# Each package asks for a slot when it measurably did, relative to its last import's release
# (Enqueued, or DepsDone in a trace that lacks it), and holds the slot for as long as it
# measurably did (the gate's grant, or Acquired, to Released). The gate is gobuild's: a freed
# slot goes to the waiting package with the highest priority, first come first served among
# equals, and it is handed over before the freed package's dependents learn it is done. Every
# package keeps its own measured durations, so the replay says what reordering alone does, not
# what cheaper or more contended execs would do. Run with python3 -I.
import argparse
import heapq
import json
import os
import re
import statistics

# Go file name suffixes that pin a file to another OS or architecture (go/build's rules).
GOOS = "aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos".split()
GOARCH = "386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm".split()


def load(walk_path, graph_path):
    w = json.load(open(walk_path))
    g = json.load(open(graph_path))
    byname = {s["Name"]: s for s in g["steps"]}
    deps = {s["Pkg"]: [byname[d]["Pkg"] for d in (s["Deps"] or []) if d in byname]
            for s in g["steps"] if not s.get("Profile")}
    steps = {s["Pkg"]: s for s in g["steps"]}
    pk = {t["Pkg"]: t for t in w["pkgs"].values()}
    return w, deps, steps, pk


def heights(deps, weight):
    """Each package's weight plus the heaviest chain of packages that import it, as gobuild's
    heightsOf computes it."""
    importers = {p: [] for p in deps}
    for p, ds in deps.items():
        for d in ds:
            importers.setdefault(d, []).append(p)
    memo = {}

    def h(p):
        if p not in memo:
            memo[p] = weight(p) + max((h(u) for u in importers.get(p, [])), default=0)
        return memo[p]
    for p in deps:
        h(p)
    return memo


def granted(t):
    return t.get("Granted") or t["Acquired"]


def asks(t):
    return t.get("Enqueued") or t["DepsDone"]


def simulate(deps, pk, prio, slots):
    """Returns the last release (ns after the first ask) and each package's replayed grant."""
    hold = {p: t["Released"] - granted(t) for p, t in pk.items()}
    lag = {}
    for p, t in pk.items():
        last = max((pk[d]["Released"] for d in deps.get(p, []) if d in pk), default=None)
        lag[p] = max(asks(t) - last, 0) if last else None
    t0 = min(asks(t) for t in pk.values())
    pending = {p: sum(1 for d in deps.get(p, []) if d in pk) for p in pk}
    importers = {}
    for p in pk:
        for d in deps.get(p, []):
            if d in pk:
                importers.setdefault(d, []).append(p)
    seq = 0
    events = []  # (time, order, kind, pkg): kind 0 = release, 1 = ask

    def push(t, kind, p):
        nonlocal seq
        seq += 1
        heapq.heappush(events, (t, kind, seq, p))
    for p in pk:
        if pending[p] == 0:
            push(asks(pk[p]) - t0, 1, p)
    waiting = []  # (-prio, arrival, pkg): first come first served among equal priorities
    arrival = 0
    free = slots
    start, end = {}, {}

    def grant(t):
        nonlocal free
        while free and waiting:
            _, _, q = heapq.heappop(waiting)
            free -= 1
            start[q] = t
            push(t + hold[q], 0, q)
    while events:
        t, kind, _, p = heapq.heappop(events)
        if kind == 0:
            end[p] = t
            free += 1
            # gobuild releases the slot (to a waiter already queued) before it closes the
            # package's result channel, so its dependents ask after that.
            grant(t)
            for u in importers.get(p, []):
                pending[u] -= 1
                if pending[u] == 0:
                    push(t + lag[u], 1, u)
        else:
            arrival += 1
            heapq.heappush(waiting, (-prio[p], arrival, p))
            grant(t)
    return max(end.values()), {p: v + t0 for p, v in start.items()}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("walk")
    ap.add_argument("graph")
    ap.add_argument("--src", help="the source tree the build compiled, to weigh workspace packages by their Go source (approximately: see real_weight)")
    ap.add_argument("--module", default="github.com/dagger/dagger")
    ap.add_argument("--goos", default="linux")
    ap.add_argument("--goarch", default="arm64")
    ap.add_argument("--slots", type=int, nargs="*", default=[16])
    a = ap.parse_args()
    w, deps, steps, pk = load(a.walk, a.graph)
    t0 = min(asks(t) for t in pk.values())
    actual = max(t["Released"] for t in pk.values()) - t0
    legacy = not all(t.get("Enqueued") for t in pk.values())
    print(f"{len(pk)} packages; measured: first package asking for a slot to last slot released {actual / 1e6:.0f} ms"
          + ("; older trace: asks are DepsDone, grants are Acquired" if legacy else ""))
    prios = {"gobuild's heights (as measured)": {p: t["Height"] for p, t in pk.items()}}
    if a.src:
        def real_weight(p):
            # gobuild's weigh: 8 KiB per step plus the bytes of the Go files the plan names. For a
            # workspace package this approximates the plan's files by the directory's non-test Go
            # files, less those whose name pins another OS or architecture; build tags inside
            # files are not evaluated.
            s = steps.get(p)
            if s is None:
                return 0
            if s.get("Tier") == "" and (p == a.module or p.startswith(a.module + "/")):
                d = os.path.join(a.src, p[len(a.module) + 1:]) if p != a.module else a.src
                size = 0
                if os.path.isdir(d):
                    for f in os.listdir(d):
                        if not f.endswith(".go") or f.endswith("_test.go"):
                            continue
                        parts = f[:-3].split("_")
                        if (parts[-1] in GOOS and parts[-1] != a.goos) or (parts[-1] in GOARCH and parts[-1] != a.goarch) or \
                                (len(parts) > 2 and parts[-2] in GOOS and parts[-2] != a.goos):
                            continue
                        size += os.path.getsize(os.path.join(d, f))
                return size + (8 << 10)
            return s["Weight"]
        prios["heights with workspace packages weighed by their Go source (approximate)"] = heights(deps, real_weight)
    hold = {p: t["Released"] - granted(t) for p, t in pk.items()}
    prios["heights from measured slot-hold times (a heuristic)"] = heights(deps, lambda p: hold.get(p, 0))
    for n in a.slots:
        for name, pr in prios.items():
            e, start = simulate(deps, pk, pr, n)
            line = f"  {n:3d} slots, {name}: {e / 1e6:7.0f} ms"
            if n == 16 and name.startswith("gobuild's"):
                # Validation: does the replay grant each package its slot when it was granted?
                err = sorted(abs(start[p] - granted(t)) / 1e6 for p, t in pk.items() if p in start)
                order_m = sorted(pk, key=lambda p: granted(pk[p]))
                order_r = sorted(start, key=lambda p: start[p])
                moved = sum(1 for i, p in enumerate(order_m) if order_r.index(p) != i) if len(order_m) == len(order_r) else None
                line += (f"  [validation: grant time error median {statistics.median(err):.1f} ms, 90th pct "
                         f"{err[int(len(err) * 0.9)]:.1f} ms, max {err[-1]:.0f} ms; {moved} packages change grant position]")
            print(line)


if __name__ == "__main__":
    main()
