# phases.py <raw dump>: durations of session phases and of outermost executed asModule/moduleSource/currentTypeDefs calls for the CLI client.
import json, sys, collections
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e
cls=lambda o:S[o.get('c',0)]
by=collections.defaultdict(list)
for o in ops.values():
    if o.get('cl'): by[o['cl']].append(o)
cli=max(by, key=lambda c: sum(cls(o).startswith('Function.') for o in by[c]))
co=by[cli]; t0=min(o['s'] for o in co); t1=max(o['d'] for o in co)
print(f"CLI client span {(t1-t0)/1e6:.0f} ms")
for o in sorted(co,key=lambda o:o['s']):
    if o.get('k')=='session_phase':
        print(f"  phase {cls(o):28s} start {(o['s']-t0)/1e6:7.0f} dur {(o['d']-o['s'])/1e6:7.0f} ms")
def top(name):
    xs=[o for o in co if o.get('k')=='call' and cls(o)==name and o.get('o')=='executed']
    # outermost only
    ids={o['id'] for o in xs}
    def outer(o):
        p=o.get('p')
        while p and p in ops:
            if p in ids: return False
            p=ops[p].get('p')
        return True
    xs=[o for o in xs if outer(o)]
    if not xs: return
    s=min(o['s'] for o in xs); e=max(o['d'] for o in xs)
    print(f"  {name}: {len(xs)} outermost executed, window {(s-t0)/1e6:.0f}-{(e-t0)/1e6:.0f} ms, summed {sum(o['d']-o['s'] for o in xs)/1e6:.0f} ms")
for n in ['ModuleSource.asModule','Query.moduleSource','Query.currentTypeDefs']: top(n)
