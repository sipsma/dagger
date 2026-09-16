from pathlib import Path
import datetime,json,subprocess,time
p=Path('/tmp/b1-b2-followup')
checks=[('followup-vet','go vet ./core/... ./dagql/... ./engine/...'),('followup-unfiltered',"env GOPATH=/home/exedev/go GOCACHE=/home/exedev/.cache/go-build go test -p=1 -exec='sudo -n --preserve-env=GOPATH,GOCACHE,PATH unshare --mount --propagation private' ./core -count=1 -v")]
results=[]
for name,cmd in checks:
 start=datetime.datetime.now(datetime.timezone.utc).isoformat();t=time.monotonic();head=subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
 print('START',name,head,flush=True)
 with (p/(name+'.log')).open('wb') as f:
  r=subprocess.run(cmd,shell=True,executable='/bin/bash',stdout=f,stderr=subprocess.STDOUT)
 data=dict(name=name,command=cmd,cwd=str(Path.cwd()),head=head,exit_status=r.returncode,seconds=round(time.monotonic()-t,3),started=start,finished=datetime.datetime.now(datetime.timezone.utc).isoformat(),log=name+'.log')
 results.append(data);(p/'followup-results.json').write_text(json.dumps(results,indent=2)+'\n')
 print('END',name,r.returncode,data['seconds'],flush=True)
