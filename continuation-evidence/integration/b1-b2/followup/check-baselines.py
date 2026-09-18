from pathlib import Path
import datetime,json,subprocess,time
p=Path('/tmp/b1-b2-followup')
cmd="env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -count=1 -v"
results=[]
for name,wd in [('integrated-original',Path.cwd()),('b2-standalone',p/'b2'),('b1-standalone',p/'b1')]:
 start=datetime.datetime.now(datetime.timezone.utc).isoformat(); t=time.monotonic()
 head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=wd,text=True).strip()
 print('START',name,head,flush=True)
 with (p/(name+'.log')).open('wb') as f:
  r=subprocess.run(cmd,shell=True,executable='/bin/bash',cwd=wd,stdout=f,stderr=subprocess.STDOUT)
 entry=dict(name=name,command=cmd,cwd=str(wd),head=head,exit_status=r.returncode,seconds=round(time.monotonic()-t,3),started=start,finished=datetime.datetime.now(datetime.timezone.utc).isoformat(),log=name+'.log')
 results.append(entry);(p/'baseline-results.json').write_text(json.dumps(results,indent=2)+'\n')
 print('END',name,r.returncode,entry['seconds'],flush=True)
