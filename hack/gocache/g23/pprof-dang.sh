# Text reports for one engine CPU profile at /p.pprof (runs in a golang container; see run.sh).
# The Dang parser recurses deeper than the profiler's stack limit, so its frames often lose their
# engine caller; the -peek at the end shows what is attributable.
pp() { echo "### go tool pprof $*"; go tool pprof "$@" /p.pprof 2>&1; echo; }
pp -top -nodecount=60
pp -top -cum -nodecount=100
pp -top -cum -nodecount=60 -focus 'vito/dang'
pp -top -cum -nodecount=60 -focus 'core/sdk/dang'
pp -peek 'dang[.]RunDir$|dang[.]ParseFile$|dang[.]ParseFileWithRecovery$|parseDirBlocks$|InferDirectoryFiles$|evaluateDirectoryFiles$|retainDangObjectDirectives$|moduleDeclaredTypeNames$|ensureModuleSelfTypes$|evalDangSource|callDangFunction$|WithNestedClientServer|FlushSessionTelemetry$|[(][*]parser[)][.]parse$'
