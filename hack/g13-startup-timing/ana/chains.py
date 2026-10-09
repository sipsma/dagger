# chains.py <raw dump> [class-regex]: for executed calls matching the regex, the ancestor class chain
# (outermost first, collapsed), counted.
import json, sys, re, collections
L=open(sys.argv[1]).read().splitlines(); S=json.loads(L[0])['strings']
ops={}
for l in L[1:]:
    e=json.loads(l)
    if e.get('e')=='op': ops[e['id']]=e
pat=re.compile(sys.argv[2] if len(sys.argv)>2 else '.')
cls=lambda o:S[o.get('c',0)]
cnt=collections.Counter()
for o in ops.values():
    if o.get('k')!='call' or o.get('o')!='executed' or not pat.search(cls(o)): continue
    chain=[]; p=o.get('p')
    while p and p in ops:
        q=ops[p]
        c=f"{q.get('k')}:{cls(q)}"
        if not chain or chain[-1]!=c: chain.append(c)
        p=q.get('p')
    chain.reverse()
    cnt[' > '.join(chain[:8])]+=1
for k,v in cnt.most_common(int(sys.argv[3]) if len(sys.argv)>3 else 12): print(v, k)
