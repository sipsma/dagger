# roots.py <raw dump>: group executed calls by their outermost *executed* ancestor call class.
import json, sys, collections
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e
cls=lambda o:S[o.get('c',0)]
ex=[o for o in ops.values() if o.get('k')=='call' and o.get('o')=='executed']
root=collections.Counter(); tops=collections.Counter()
for o in ex:
    r=o; p=o.get('p')
    while p and p in ops:
        q=ops[p]
        if q.get('k')=='call' and q.get('o')=='executed': r=q
        p=q.get('p')
    root[cls(r)]+=1
    if r is o: tops[cls(o)]+=1
print("executed:",len(ex)); print("by outermost executed ancestor:"); [print(" ",v,k) for k,v in root.most_common(15)]
print("outermost executed calls themselves:"); [print(" ",v,k) for k,v in tops.most_common(15)]
