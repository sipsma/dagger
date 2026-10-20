# Text reports for one engine CPU profile at /p.pprof (runs in a golang container; see run.sh).
# The Dang parser recurses deeper than the profiler's stack limit, so parser frames often lose their
# engine caller: the '(*parser)' focus counts parser CPU wherever its stack was cut.
pp() { echo "### go tool pprof $*"; go tool pprof "$@" /p.pprof 2>&1; echo; }
pp -top -nodecount=40
pp -top -cum -nodecount=80
pp -top -cum -nodecount=30 -focus 'vito/dang/v2/pkg/dang[.][(][*]parser[)]'
pp -top -cum -nodecount=40 -focus 'vito/dang'
pp -peek 'dang[.]RunDir$|parseDirBlocks$|ParseFileWithRecovery$|dang[.]ParseFile$|InferDirectoryFiles$|evaluateDirectoryFiles$|moduleDeclaredTypeNames$|ensureModuleSelfTypes$|callDangFunction$'
