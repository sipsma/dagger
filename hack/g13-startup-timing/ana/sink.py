# sink.py <raw dump> [class]: for the CLI client, self-time (dur minus children union) summed by class+kind+outcome, top N.
import json, sys, collections
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}; kids=collections.defaultdict(list)
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e; kids[e.get('p')].append(e)
cls=lambda o:S[o.get('c',0)]
by=collections.defaultdict(list)
for o in ops.values():
    if o.get('cl'): by[o['cl']].append(o)
cli=max(by, key=lambda c: sum(cls(o).startswith('Function.') for o in by[c]))
def union(iv):
    iv=sorted(iv); tot=0; cs=ce=None
    for s,e in iv:
        if cs is None or s>ce:
            if cs is not None: tot+=ce-cs
            cs,ce=s,e
        else: ce=max(ce,e)
    if cs is not None: tot+=ce-cs
    return tot
agg=collections.Counter(); n=collections.Counter()
for o in by[cli]:
    ch=[(c['s'],c['d']) for c in kids[o['id']]]
    self_t=(o['d']-o['s'])-union(ch)
    key=f"{o.get('k')}:{cls(o)}[{o.get('o')}]"
    agg[key]+=self_t; n[key]+=1
tot=sum(agg.values())
print(f"total self {tot/1e6:.0f} ms over {len(by[cli])} ops")
for k,v in agg.most_common(int(sys.argv[2]) if len(sys.argv)>2 else 25): print(f"{v/1e6:8.0f} ms {n[k]:6d}x {k}")
