# Text reports for one engine CPU profile at /p.pprof (runs in a golang container; see run.sh).
pp() { echo "### go tool pprof $*"; go tool pprof "$@" /p.pprof 2>&1; echo; }
SQ='[(][*]Server[)][.]serveQuery'
pp -top -nodecount=60
pp -top -cum -nodecount=80
pp -top -cum -nodecount=150 -focus "$SQ"
pp -top -nodecount=80 -focus "$SQ"
pp -peek 'executor[.][(][*]Executor[)][.](parseQuery|CreateOperationContext)$|dagql[.][(][*]Server[)][.](Exec|ExecOp|parseASTSelections|Resolve)$|preselect$|resultCallArgFromInput$|resultCallRefFromResult$|AroundFunc|transport[.]POST[.]Do$'
