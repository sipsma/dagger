# tree.py <raw dump> <op-id|class> [depth]: children timeline of an op (first executed outermost op of class if a class is given).
import json, sys, collections
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}; kids=collections.defaultdict(list)
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e; kids[e.get('p')].append(e)
cls=lambda o:S[o.get('c',0)]
arg=sys.argv[2]; depth=int(sys.argv[3]) if len(sys.argv)>3 else 2
if arg.isdigit(): root=ops[int(arg)]
else:
    xs=sorted([o for o in ops.values() if cls(o)==arg and o.get('k')=='call_exec'], key=lambda o:-(o['d']-o['s']))
    root=xs[int(sys.argv[4]) if len(sys.argv)>4 else 0]
def show(o,d,t0):
    ch=kids[o['id']]
    agg=collections.defaultdict(lambda:[0,0.0,1e18,0])
    for c in ch:
        a=agg[(c.get('k'),cls(c),c.get('o'))]; a[0]+=1; a[1]+=(c['d']-c['s'])/1e6; a[2]=min(a[2],c['s']); a[3]=max(a[3],c['d'])
    for (k,c,oc),a in sorted(agg.items(), key=lambda kv: kv[1][2]):
        print("  "*d+f"{a[0]:4d}x {k}:{c} [{oc}] sum {a[1]:.0f} ms window {(a[2]-t0)/1e6:.0f}-{(a[3]-t0)/1e6:.0f}")
print(f"root {root.get('k')}:{cls(root)} dur {(root['d']-root['s'])/1e6:.0f} ms id {root['id']}")
show(root,1,root['s'])
