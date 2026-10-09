# nsstep.py <raw dump>: per executed asModule (call_exec), time spent after its definition lookup
# (namespacing + patch: the window of its direct-child __with*/sourceMap calls) vs its total.
import json, sys, collections, re
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}; kids=collections.defaultdict(list)
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e; kids[e.get('p')].append(e)
cls=lambda o:S[o.get('c',0)]
pat=re.compile(r'\.__with|^Query\.sourceMap$')
tot=ns=0; rows=[]
for o in ops.values():
    if o.get('k')!='call_exec' or cls(o)!='ModuleSource.asModule': continue
    ch=[c for c in kids[o['id']] if pat.search(cls(c))]
    d=(o['d']-o['s'])/1e6
    w=(max(c['d'] for c in ch)-min(c['s'] for c in ch))/1e6 if ch else 0
    tot+=d; ns+=w; rows.append((d,w,len(ch)))
rows.sort(reverse=True)
print(f"asModule execs {len(rows)}: summed dur {tot:.0f} ms, summed namespacing window {ns:.0f} ms")
for d,w,n in rows[:8]: print(f"  dur {d:6.0f} ms  ns-window {w:5.0f} ms  ({n} direct builder calls)")
