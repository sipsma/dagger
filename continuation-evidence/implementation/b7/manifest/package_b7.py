# Batch 7 has two authors and five merges, so it cannot be packaged by copying
# each commit's tree as package.py does for batches 1 to 6. Each non-merge
# commit is instead applied as its own change (a three-way merge of its
# evidence-stripped tree against its parent's, done on git objects only) on top
# of the packaged batch 6 head, in topological order. A commit that changes
# nothing after stripping, or whose change is already present (author A's -x
# copies of author B's fixes, a repair and its revert once both are applied),
# is omitted. The final tree must equal the integrated tip's stripped tree.
import subprocess,os,json,re,sys
def run(*a,env=None,inp=None,check=True):
    return subprocess.run(a,check=check,capture_output=True,text=True,env=env,input=inp)
def sh(*a,**k): return run(*a,**k).stdout
EVID=["continuation-evidence","cleanup-evidence","cleanup-review-evidence","CLEANUP-IMPLEMENTATION.md"]
BASE="c5b299142ca672cbd2ef0a389492f11de85ff08b"
HEAD=sh("git","rev-parse",sys.argv[1]).strip()
ONTO=sh("git","rev-parse","b7-packaging/remote-cache/b6-sharing").strip()
BRANCH="b7-packaging/remote-cache/b7-verification"
def stripped_tree(commit):
    keep=[l for l in sh("git","ls-tree",commit).split("\n") if l and l.split("\t",1)[1] not in EVID]
    return sh("git","mktree",inp="\n".join(keep)+"\n").strip()
ENV=dict(os.environ,GIT_AUTHOR_NAME="x",GIT_AUTHOR_EMAIL="x@x",GIT_AUTHOR_DATE="2026-01-01T00:00:00Z",GIT_COMMITTER_NAME="x",GIT_COMMITTER_EMAIL="x@x",GIT_COMMITTER_DATE="2026-01-01T00:00:00Z")
_shadow={}
def shadow(tree):
    # a parentless throwaway commit, only so merge-tree can be given the tree
    if tree not in _shadow:
        _shadow[tree]=sh("git","commit-tree",tree,env=ENV,inp="shadow\n").strip()
    return _shadow[tree]
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
assert sh("git","rev-parse",ONTO+"^{tree}").strip()==stripped_tree(BASE)
revs=sh("git","rev-list","--reverse","--topo-order","--no-merges",f"{BASE}..{HEAD}").split()
parent=ONTO; cur=stripped_tree(BASE); mapping={}; omitted=[]; conflicts=[]
for c in revs:
    before=stripped_tree(c+"^"); after=stripped_tree(c)
    if before==after:
        mapping[c]=None; omitted.append((c,"evidence only")); continue
    r=run("git","merge-tree","--write-tree","--merge-base="+shadow(before),shadow(cur),shadow(after),check=False)
    new=r.stdout.split("\n")[0].strip()
    if r.returncode!=0:
        conflicts.append((c,r.stdout)); print("CONFLICT",c[:10],sh("git","log","-1","--format=%s",c).strip()); print(r.stdout[:1500]); sys.exit(1)
    if new==cur:
        mapping[c]=None; omitted.append((c,"change already present")); continue
    f=sh("git","log","-1","--format=%an%n%ae%n%aI",c).split("\n")
    env=dict(os.environ,GIT_AUTHOR_NAME=f[0],GIT_AUTHOR_EMAIL=f[1],GIT_AUTHOR_DATE=f[2],GIT_COMMITTER_NAME=f[0],GIT_COMMITTER_EMAIL=f[1],GIT_COMMITTER_DATE=f[2])
    parent=sh("git","commit-tree",new,"-p",parent,env=env,inp=message(c)).strip()
    mapping[c]=parent; cur=new
want=stripped_tree(HEAD)
print("kept",sum(1 for v in mapping.values() if v),"omitted",len(omitted),"head",parent[:10],"tree equal",cur==want)
if cur!=want:
    print(sh("git","diff","--stat",cur,want)); sys.exit(2)
sh("git","update-ref","refs/heads/"+BRANCH,parent)
json.dump({"branch":BRANCH,"originalParent":BASE,"originalHead":HEAD,"onto":ONTO,"packagedHead":parent,"packagedTree":cur,
 "commits":sum(1 for v in mapping.values() if v),"omitted":[{"commit":c,"why":w,"subject":sh("git","log","-1","--format=%s",c).strip()} for c,w in omitted],"map":mapping},open("/tmp/b7/pkg7/result.json","w"),indent=1)
