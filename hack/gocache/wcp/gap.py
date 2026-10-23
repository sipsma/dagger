#!/usr/bin/env python3
# wcp: where a dagger.io/go first build's time goes, package by package, from a wcprof dump of the
# build and, for the old (tiered) build, the per-package tier traces its measurement build writes.
#
#   gap.py build  <dump> [--trace DIR] [--graph graph.json] [--json out.json]
#       the build's critical chain over the plan's real import graph, segment by segment;
#   gap.py compare <old.json> <new.json>
#       two builds' chains and packages side by side (from "build --json" outputs).
#
# The chain is walked back from the link: at each package, the import whose result arrived last
# (in the old build's tier, the dependency whose end made the package ready, as gohdr recorded it).
# Times are milliseconds from the build's first recorded op. Run with python3 -I.
import argparse
import collections
import json
import os
import re
import sys

MS = 1e6


def load_dump(path):
    with open(path) as f:
        h = json.loads(f.readline())
        S = h["strings"]
        ops, waits = {}, []
        for line in f:
            e = json.loads(line)
            if e["e"] == "op":
                ops[e["id"]] = e
            elif e["e"] == "wait":
                waits.append(e)
    for e in ops.values():
        e["cls"] = S[e["c"]] if e.get("c") else ""
        e["meta"] = S[e["m"]] if e.get("m") else ""
    return h, S, ops, waits


RE_PKG = re.compile(r"^set -e\n# ([^\n]+)\n")
RE_C = re.compile(r"\s-c=(\d+)")


def classify(argv):
    """(kind, key) for a processRun's argv: ("pkg", import path), ("tier", ""), ("modunzip",
    module@version) or ("other", first words)."""
    if len(argv) >= 3 and argv[0] == "sh" and argv[1] == "-c":
        m = RE_PKG.match(argv[2])
        if m:
            return "pkg", m.group(1)
        if "gohdr modunzip" in argv[2]:
            mm = re.search(r"modunzip \S+ '([^']+)'", argv[2])
            return "modunzip", mm.group(1) if mm else "?"
    if len(argv) >= 2 and argv[0].endswith("/gohdr") and argv[1] == "run":
        return "tier", ""
    return "other", " ".join(argv[:3])[:80]


class Build:
    def __init__(self, dump, trace_dir=None, graph=None):
        self.h, self.S, self.ops, self.waits = load_dump(dump)
        self.epoch = self.h["epoch_unix_nano"]
        starts = [e["s"] for e in self.ops.values()]
        self.t0 = min(starts)
        self.tend = max(e["d"] for e in self.ops.values())
        # An engine with exec CPU recording puts a zero-length exec.cgroupCPU op under each
        # processRun, its metadata the exec cgroup's CPU counters.
        cpu = {}
        for e in self.ops.values():
            if e["cls"] == "exec.cgroupCPU" and e["meta"]:
                cpu[e.get("p")] = json.loads(e["meta"])
        self.execs = []  # one per processRun
        for e in self.ops.values():
            if e["cls"] != "exec.processRun":
                continue
            try:
                argv = json.loads(e["meta"]) if e["meta"] else []
            except ValueError:
                argv = []
            kind, key = classify(argv)
            x = {"kind": kind, "key": key, "proc_s": e["s"], "proc_e": e["d"], "cpu": cpu.get(e["id"])}
            a = self.ops.get(e.get("p"))
            while a:
                if a["cls"] == "exec.run" and "exec_s" not in x:
                    x["exec_s"], x["exec_e"] = a["s"], a["d"]
                if a.get("k") == "lazy" and a["cls"] == "Container.withExec" and "lazy_s" not in x:
                    x["lazy_s"], x["lazy_e"] = a["s"], a["d"]
                parent = self.ops.get(a.get("p"))
                if parent and parent["cls"] == "session.query":
                    x["req_s"], x["req_e"], x["req_cls"] = a["s"], a["d"], a["cls"]
                a = parent
            x.setdefault("exec_s", e["s"])
            x.setdefault("lazy_s", x["exec_s"])
            x.setdefault("req_s", x["lazy_s"])
            x.setdefault("req_e", e["d"])
            self.execs.append(x)
        self.pkgs = {}
        for x in self.execs:
            if x["kind"] == "pkg":
                # One exec per package in a binary build; keep the first if a package repeats.
                self.pkgs.setdefault(x["key"], x)
        self.tiers = [x for x in self.execs if x["kind"] == "tier"]
        self.trace = {}  # tier package import path -> trace line, with "tier" index
        self.tier_runs = []
        if trace_dir:
            self._load_traces(trace_dir)
        self.graph = None
        if graph:
            with open(graph) as f:
                g = json.load(f)
            byname = {s["Name"]: s for s in g["steps"]}
            self.graph = {s["Pkg"]: [byname[d]["Pkg"] for d in s["Deps"] if d in byname]
                          for s in g["steps"] if not s.get("Profile")}
            self.link_pkg = next(s["Pkg"] for s in g["steps"] if s.get("Link"))

    def rel(self, ns_since_epoch):
        return (ns_since_epoch - self.t0) / MS

    def unix_rel(self, unix_ns):
        return (unix_ns - self.epoch - self.t0) / MS

    def _load_traces(self, d):
        files = sorted(f for f in os.listdir(d) if f.endswith(".jsonl"))
        runs = []
        for f in files:
            lines = [json.loads(l) for l in open(os.path.join(d, f)) if l.strip()]
            run = next(l for l in lines if l.get("run"))
            nodes = [l for l in lines if not l.get("run")]
            runs.append((run, nodes, f))
        # Match each trace to its tier exec by time: the exec whose process ran around it.
        for run, nodes, f in runs:
            rs = run["start"] - self.epoch
            best = min(self.tiers, key=lambda x: abs(x["proc_s"] - rs)) if self.tiers else None
            info = {"file": f, "run": run, "nodes": nodes, "exec": best,
                    "align_ms": (rs - best["proc_s"]) / MS if best else None}
            self.tier_runs.append(info)
            byname = {n["name"]: n for n in nodes}
            for n in nodes:
                n["tier"] = len(self.tier_runs) - 1
                n["after_pkg"] = byname[n["ready_after"]]["pkg"] if n.get("ready_after") in byname else ""
                self.trace[n["pkg"]] = n

    # Each package's result time as its importers see it, and its own request start.
    def done(self, pkg):
        if pkg in self.pkgs:
            return self.pkgs[pkg]["req_e"]
        if pkg in self.trace:
            return self.tier_runs[self.trace[pkg]["tier"]]["exec"]["req_e"]
        return None

    def chain(self):
        """The critical chain, walked back from the link, as segments (label, start, end, cat),
        in ns since epoch, earliest first."""
        if not self.graph:
            raise SystemExit("chain needs --graph")
        segs = []
        cur = self.link_pkg
        while cur is not None:
            if cur in self.pkgs:
                x = self.pkgs[cur]
                segs.append((cur, x["proc_e"], x["req_e"], "after compile (outputs, result)"))
                segs.append((cur, x["proc_s"], x["proc_e"], "compile (process)"))
                segs.append((cur, x["exec_s"], x["proc_s"], "container start"))
                segs.append((cur, x["lazy_s"], x["exec_s"], "exec setup (mounts, inputs)"))
                segs.append((cur, x["req_s"], x["lazy_s"], "request to exec"))
                deps = [(self.done(d), d) for d in self.graph.get(cur, []) if self.done(d) is not None]
                if not deps:
                    segs.append(("(before first package)", self.t0, x["req_s"], "before the first compile"))
                    break
                t, d = max(deps)
                if d in self.trace and cur not in self.trace:
                    # A workspace package waiting on a whole tier: into the tier.
                    tr = self.tier_runs[self.trace[d]["tier"]]
                    segs.append((cur, tr["exec"]["req_e"], x["req_s"], "hand-off (module, slot)"))
                    cur = ("tier", self.trace[d]["tier"])
                    continue
                segs.append((cur, t, x["req_s"], "hand-off (module, slot)"))
                cur = d
                continue
            # In a tier.
            ti = cur[1] if isinstance(cur, tuple) else self.trace[cur]["tier"]
            tr = self.tier_runs[ti]
            ex = tr["exec"]
            e0 = self.epoch
            if isinstance(cur, tuple):
                segs.append((f"tier {ti}", ex["proc_e"], ex["req_e"], "after compile (outputs, result)"))
                last = max(tr["nodes"], key=lambda n: n["linked"])
                segs.append((f"tier {ti} end", tr["run"]["end"] - e0, ex["proc_e"], "tier: write trace, exit"))
                segs.append((f"tier {ti} end", last["linked"] - e0, tr["run"]["end"] - e0, "tier: tail after last package"))
                cur = last["pkg"]
                continue
            n = self.trace[cur]
            segs.append((cur, n["end"] - e0, n["linked"] - e0, "tier: link archive"))
            segs.append((cur, n["start"] - e0, n["end"] - e0, "compile (process)"))
            segs.append((cur, n["dispatched"] - e0 if n.get("dispatched") else n["start"] - e0, n["start"] - e0, "tier: script prep"))
            if n.get("dispatched"):
                segs.append((cur, n["ready"] - e0, n["dispatched"] - e0, "tier: slot wait"))
            if n.get("after_pkg"):
                prev = self.trace[n["after_pkg"]]
                segs.append((cur, prev["linked"] - e0, n["ready"] - e0, "tier: hand-off"))
                cur = n["after_pkg"]
                continue
            # A root of the tier: from the tier's request.
            segs.append((f"tier {ti} start", ex["proc_s"], tr["run"]["start"] - e0, "tier: process start, read spec"))
            segs.append((f"tier {ti} start", ex["exec_s"], ex["proc_s"], "container start"))
            segs.append((f"tier {ti} start", ex["lazy_s"], ex["exec_s"], "exec setup (mounts, inputs)"))
            segs.append((f"tier {ti} start", ex["req_s"], ex["lazy_s"], "request to exec"))
            # What the tier waited for: a lower tier's result, or the plan.
            lower = [r for r in self.tier_runs if r is not tr and r["exec"]["req_e"] <= ex["req_s"]]
            if lower:
                lo = max(lower, key=lambda r: r["exec"]["req_e"])
                segs.append((f"tier {ti} start", lo["exec"]["req_e"], ex["req_s"], "hand-off (module, slot)"))
                cur = ("tier", self.tier_runs.index(lo))
                continue
            segs.append(("(before first package)", self.t0, ex["req_s"], "before the first compile"))
            cur = None
        segs.reverse()
        return segs

    def summary(self):
        out = {"t0_unix": self.epoch + self.t0, "span_ms": (self.tend - self.t0) / MS,
               "execs": collections.Counter(x["kind"] for x in self.execs)}
        segs = self.chain() if self.graph else []
        bycat = collections.defaultdict(float)
        for _, s, e, c in segs:
            bycat[c] += (e - s) / MS
        out["chain"] = [{"pkg": l, "s": self.rel(s), "e": self.rel(e), "ms": (e - s) / MS, "cat": c} for l, s, e, c in segs]
        out["chain_by_cat"] = dict(sorted(bycat.items(), key=lambda kv: -kv[1]))
        out["chain_end_ms"] = self.rel(segs[-1][2]) if segs else None
        pk = {}
        for p, x in self.pkgs.items():
            pk[p] = {"compile_ms": (x["proc_e"] - x["proc_s"]) / MS, "req_ms": (x["req_e"] - x["req_s"]) / MS,
                     "where": "exec", "start": self.rel(x["proc_s"])}
            if x["cpu"]:
                pk[p]["cpu_ms"] = x["cpu"]["usage_usec"] / 1e3
        for p, n in self.trace.items():
            pk[p] = {"compile_ms": (n["end"] - n["start"]) / MS, "where": f"tier {n['tier']}",
                     "cpu_ms": (n["user_us"] + n["sys_us"]) / 1e3, "c": n["c"],
                     "start": self.unix_rel(n["start"]), "slot_wait_ms": (n["dispatched"] - n["ready"]) / MS if n.get("dispatched") else None}
        out["packages"] = pk
        out["tiers"] = [{"file": t["file"], "nodes": len(t["nodes"]), "jobs": t["run"]["jobs"],
                         "run_ms": (t["run"]["end"] - t["run"]["start"]) / MS,
                         "exec_req_ms": (t["exec"]["req_e"] - t["exec"]["req_s"]) / MS,
                         "req_s": self.rel(t["exec"]["req_s"]), "req_e": self.rel(t["exec"]["req_e"]),
                         "align_ms": t["align_ms"], "cpu_s": sum(n["user_us"] + n["sys_us"] for n in t["nodes"]) / 1e6,
                         "busy": busy_profile([(n["start"], n["end"]) for n in t["nodes"]])}
                        for t in self.tier_runs]
        out["exec_busy"] = busy_profile([(x["proc_s"], x["proc_e"]) for x in self.execs if x["kind"] == "pkg"])
        return out


def busy_profile(intervals):
    """Mean and peak number of intervals running at once, over their own span."""
    if not intervals:
        return None
    ev = sorted([(s, 1) for s, _ in intervals] + [(e, -1) for _, e in intervals])
    cur = peak = 0
    area = 0.0
    last = ev[0][0]
    for t, d in ev:
        area += cur * (t - last)
        last = t
        cur += d
        peak = max(peak, cur)
    span = ev[-1][0] - ev[0][0]
    return {"mean": area / span if span else 0, "peak": peak, "span_ms": span / MS}


def print_build(s, top=60):
    print(f"span {s['span_ms']:.0f} ms; execs {dict(s['execs'])}")
    for t in s["tiers"]:
        print(f"tier {t['file']}: {t['nodes']} packages, NumCPU {t['jobs']}, request {t['req_s']:.0f}..{t['req_e']:.0f} ms "
              f"({t['exec_req_ms']:.0f} ms), gohdr run {t['run_ms']:.0f} ms, CPU {t['cpu_s']:.1f} s, "
              f"scripts at once mean {t['busy']['mean']:.1f} peak {t['busy']['peak']}; trace-to-exec alignment {t['align_ms']:.1f} ms")
    if s["exec_busy"]:
        b = s["exec_busy"]
        print(f"package execs: processes at once mean {b['mean']:.1f} peak {b['peak']} over {b['span_ms']:.0f} ms")
    if not s["chain"]:
        return
    print(f"\ncritical chain, ends at {s['chain_end_ms']:.0f} ms:")
    for c, ms in s["chain_by_cat"].items():
        print(f"  {ms:8.0f} ms  {c}")
    print("\nchain, segments over 20 ms:")
    for c in s["chain"]:
        if c["ms"] >= 20:
            print(f"  {c['s']:7.0f} {c['ms']:7.0f} ms  {c['cat']:<34} {c['pkg']}")


def compare(a, b):
    print("chain by category (ms): old, new, new-old")
    cats = sorted(set(a["chain_by_cat"]) | set(b["chain_by_cat"]), key=lambda c: -(b["chain_by_cat"].get(c, 0) - a["chain_by_cat"].get(c, 0)))
    for c in cats:
        x, y = a["chain_by_cat"].get(c, 0), b["chain_by_cat"].get(c, 0)
        print(f"  {x:8.0f} {y:8.0f} {y - x:+8.0f}  {c}")
    print(f"  {a['chain_end_ms']:8.0f} {b['chain_end_ms']:8.0f} {b['chain_end_ms'] - a['chain_end_ms']:+8.0f}  chain end")
    onchain = {c["pkg"] for c in a["chain"] if c["cat"] == "compile (process)"} | {c["pkg"] for c in b["chain"] if c["cat"] == "compile (process)"}
    ca = {c["pkg"] for c in a["chain"] if c["cat"] == "compile (process)"}
    cb = {c["pkg"] for c in b["chain"] if c["cat"] == "compile (process)"}
    print("\npackages compiled on either chain: compile ms old / new (old: CPU ms, -c); on chain old/new")
    rows = []
    for p in onchain:
        pa, pb = a["packages"].get(p, {}), b["packages"].get(p, {})
        rows.append((pb.get("compile_ms", 0) - pa.get("compile_ms", 0), p, pa, pb))
    for d, p, pa, pb in sorted(rows, key=lambda r: -max(r[2].get("compile_ms", 0), r[3].get("compile_ms", 0))):
        print(f"  {pa.get('compile_ms', 0):7.0f} {pb.get('compile_ms', 0):7.0f} {d:+7.0f}  cpu {pa.get('cpu_ms', 0):6.0f} c={pa.get('c', '-')}"
              f"  {'O' if p in ca else '.'}{'N' if p in cb else '.'}  {p}")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("dump")
    b.add_argument("--trace")
    b.add_argument("--graph")
    b.add_argument("--json")
    c = sub.add_parser("compare")
    c.add_argument("old")
    c.add_argument("new")
    a = ap.parse_args()
    if a.cmd == "build":
        s = Build(a.dump, a.trace, a.graph).summary()
        if a.json:
            with open(a.json, "w") as f:
                json.dump(s, f)
        print_build(s)
    else:
        compare(json.load(open(a.old)), json.load(open(a.new)))


if __name__ == "__main__":
    sys.exit(main())
