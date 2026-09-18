import subprocess,os,json,re,sys
def sh(*a,env=None,inp=None):
    return subprocess.run(a,check=True,capture_output=True,text=True,env=env,input=inp).stdout
EVID=["continuation-evidence","cleanup-evidence","cleanup-review-evidence","CLEANUP-IMPLEMENTATION.md"]
PREFIX="b7-packaging/"
SEGS=[("remote-cache/transfer-foundations","a7d4bad2290b8a32e4fde16fb7d940a78c46d0b2","1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f"),
("remote-cache/b1-producers","1ca9f28a60f1d9597c1b0df01e65a91707ce3b0f","a88e0e0cdde5383d085347c55288ff84651239e2"),
("remote-cache/b2-transfer","a88e0e0cdde5383d085347c55288ff84651239e2","77f6279559061fd1bb6b3b18e6b08582c7b013a3"),
("remote-cache/b4-acquisition","77f6279559061fd1bb6b3b18e6b08582c7b013a3","fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5"),
("remote-cache/b5-offers","fd9cfd55a98b176bf82cf84ee1b4c3ae1257fde5","a26dc93750e42daf2de76678b0e54f51454cea33"),
("remote-cache/b6-sharing","a26dc93750e42daf2de76678b0e54f51454cea33","c5b299142ca672cbd2ef0a389492f11de85ff08b")]
IDX="/tmp/b7/pkg/index"
def stripped_tree(commit):
    # every evidence path is a top-level entry, so the stripped tree is the
    # root tree without those entries
    keep=[l for l in sh("git","ls-tree",commit).split("\n") if l and l.split("\t",1)[1] not in EVID]
    return sh("git","mktree",inp="\n".join(keep)+"\n").strip()
DROP=re.compile(r"^(co-authored-by:|🤖|generated with)",re.I)
def message(commit):
    raw=sh("git","log","-1","--format=%B",commit)
    lines=[l for l in raw.rstrip("\n").split("\n") if not DROP.match(l.strip())]
    while lines and not lines[-1].strip(): lines.pop()
    sob=[l for l in lines if l.lower().startswith("signed-off-by:")]
    body=[l for l in lines if not l.lower().startswith("signed-off-by:")]
    while body and not body[-1].strip(): body.pop()
    if not sob:
        sob=["Signed-off-by: "+sh("git","log","-1","--format=%an <%ae>",commit).strip()]
    seen=[]; [seen.append(s) for s in sob if s not in seen]
    return "\n".join(body)+"\n\n"+"\n".join(seen)+"\n"
mapping={}; report=[]
parent=SEGS[0][1]; mapping[parent]=parent
for name,base,head in SEGS:
    revs=sh("git","rev-list","--reverse",f"{base}..{head}").split()
    assert sh("git","rev-list","--merges","--count",f"{base}..{head}").strip()=="0"
    kept=dropped=0; prev_tree=sh("git","rev-parse",parent+"^{tree}").strip() if parent!=SEGS[0][1] else stripped_tree(parent)
    # the public foundation head carries no evidence paths, so its stripped tree is its tree
    for c in revs:
        tree=stripped_tree(c)
        if tree==prev_tree:
            mapping[c]=None; dropped+=1; continue
        f=sh("git","log","-1","--format=%an%n%ae%n%aI",c).split("\n")
        env=dict(os.environ,GIT_AUTHOR_NAME=f[0],GIT_AUTHOR_EMAIL=f[1],GIT_AUTHOR_DATE=f[2],GIT_COMMITTER_NAME=f[0],GIT_COMMITTER_EMAIL=f[1],GIT_COMMITTER_DATE=f[2])
        new=sh("git","commit-tree",tree,"-p",parent,env=env,inp=message(c)).strip()
        mapping[c]=new; parent=new; prev_tree=tree; kept+=1
    sh("git","update-ref","refs/heads/"+PREFIX+name,parent)
    mapping["head:"+head]=parent
    report.append({"branch":PREFIX+name,"originalParent":base,"originalHead":head,"packagedHead":parent,"packagedTree":prev_tree,"commits":kept,"omitted":dropped})
    print(name,"kept",kept,"omitted",dropped,"head",parent[:10])
json.dump({"branches":report,"map":mapping},open("/tmp/b7/pkg/result.json","w"),indent=1)
