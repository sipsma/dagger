#!/usr/bin/env python3
# wcp: replay a per-package dagger.io/go build through gobuild's exec-slot gate under other
# priorities or slot counts, from one measured build of today's (per-package) build.
#
#   slotsim.py <walk.json> <graph.json> [--src DIR] [--slots N ...]
#
# Each package holds a slot for as long as it measurably did (Acquired to Released in the walk
# trace: helper work, its requests, exec setup, compile and outputs), becomes ready when its
# last import's slot was released plus the measured hand-off, and the gate gives a freed slot to
# the waiting package with the highest priority. Packages ready before any slot was held start at
# their measured times. The replay keeps every package's own duration as measured, so it says
# what reordering alone does, not what cheaper execs would do. Run with python3 -I.
import argparse
import heapq
import json
import os


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


def simulate(deps, pk, prio, slots):
    hold = {p: t["Released"] - t["Acquired"] for p, t in pk.items()}
    # Measured gap between the last import's release and this package asking for a slot.
    lag = {}
    for p, t in pk.items():
        last = max((pk[d]["Released"] for d in deps.get(p, []) if d in pk), default=None)
        lag[p] = max(t["DepsDone"] - last, 0) if last else 0
    t0 = min(t["DepsDone"] for t in pk.values())
    roots_at = {p: pk[p]["DepsDone"] - t0 for p in pk if not any(d in pk for d in deps.get(p, []))}
    pending = {p: sum(1 for d in deps.get(p, []) if d in pk) for p in pk}
    importers = {}
    for p in pk:
        for d in deps.get(p, []):
            if d in pk:
                importers.setdefault(d, []).append(p)
    events = []  # (time, kind, pkg): kind 0 = release, 1 = ready
    for p, at in roots_at.items():
        heapq.heappush(events, (at, 1, p))
    waiting = []  # (-prio, pkg)
    free = slots
    end = {}
    while events:
        t, kind, p = heapq.heappop(events)
        if kind == 0:
            free += 1
            end[p] = t
            for u in importers.get(p, []):
                pending[u] -= 1
                if pending[u] == 0:
                    heapq.heappush(events, (t + lag[u], 1, u))
        else:
            heapq.heappush(waiting, (-prio[p], p))
        while free and waiting and (not events or events[0][0] > t):
            _, q = heapq.heappop(waiting)
            free -= 1
            heapq.heappush(events, (t + hold[q], 0, q))
    return max(end.values()) / 1e6, len(end)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("walk")
    ap.add_argument("graph")
    ap.add_argument("--src", help="the source tree the build compiled, to weigh workspace packages by their real Go source")
    ap.add_argument("--module", default="github.com/dagger/dagger")
    ap.add_argument("--slots", type=int, nargs="*", default=[16])
    a = ap.parse_args()
    w, deps, steps, pk = load(a.walk, a.graph)
    t0 = min(t["DepsDone"] for t in pk.values())
    actual = (max(t["Released"] for t in pk.values()) - t0) / 1e6
    print(f"{len(pk)} packages; measured: first package ready to last slot released {actual:.0f} ms")
    prios = {"gobuild's heights (as measured)": {p: t["Height"] for p, t in pk.items()}}
    if a.src:
        def real_weight(p):
            s = steps.get(p)
            if s is None:
                return 0
            if s.get("Tier") == "" and (p == a.module or p.startswith(a.module + "/")):
                d = os.path.join(a.src, p[len(a.module) + 1:]) if p != a.module else a.src
                size = sum(os.path.getsize(os.path.join(d, f)) for f in os.listdir(d)
                           if f.endswith(".go") and not f.endswith("_test.go")) if os.path.isdir(d) else 0
                return 8 << 10 if size == 0 else size + (8 << 10)
            return s["Weight"]
        prios["heights with workspace packages weighed by their real source"] = heights(deps, real_weight)
    hold = {p: t["Released"] - t["Acquired"] for p, t in pk.items()}
    prios["heights from measured slot-hold times (best case)"] = heights(deps, lambda p: hold.get(p, 0))
    for n in a.slots:
        for name, pr in prios.items():
            e, done = simulate(deps, pk, pr, n)
            print(f"  {n:3d} slots, {name}: {e:7.0f} ms ({done} packages)")


if __name__ == "__main__":
    main()
