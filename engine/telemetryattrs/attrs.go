package telemetryattrs

const (
	UIResumeOutputAttr = "dagger.io/ui.resume.output"

	// DagBlockedAttr marks a lazy-evaluation resume span that aborted because a
	// prerequisite result's evaluation failed, rather than because the result's
	// own deferred work failed. The UI treats a blocked resumption as if the
	// deferred work never ran: the owning API spans return to pending instead
	// of being marked caused-failed.
	DagBlockedAttr = "dagger.io/dag.blocked"

	// Streaming progress over OTel logs.
	//
	// A log record carrying ProgressItemAttr is progress data, not log text:
	// it reports absolute completion for one named item of work (a layer
	// being fetched, a file being transferred) within the span the record is
	// attached to. The TUI folds these records into progress bars instead of
	// rendering them as logs.
	//
	// Records are keyed by (span, item): each new record replaces the item's
	// previous state, so emitters can throttle freely and consumers only keep
	// the latest values.

	// ProgressItemAttr uniquely names the item within its span, e.g. a layer
	// digest. (string)
	ProgressItemAttr = "dagger.io/progress.item"
	// ProgressCurrentAttr is the item's absolute completed amount. (int64)
	ProgressCurrentAttr = "dagger.io/progress.current"
	// ProgressTotalAttr is the item's expected final amount. Zero or absent
	// means the total is unknown (indeterminate). (int64)
	ProgressTotalAttr = "dagger.io/progress.total"
	// ProgressUnitAttr optionally names the unit of current/total, e.g.
	// "bytes", for human-readable display. (string)
	ProgressUnitAttr = "dagger.io/progress.unit"
)

// wcprof × OTel vocabulary.
//
// These attributes let the engine emit, on its ordinary OTel spans, the
// wait-edge / shared-execution / causal-parent information that the native
// wcprof recorder records inline (engine/wcprof). They are the *only* new
// vocabulary the OTel profiling source introduces; everything else reuses
// existing dagger.io/* attributes. A single definition is shared by the
// offline loader (engine/wcprof/wcotel) and the engine emit sites, so the two
// can never diverge on a key or an encoding. See hack/designs/wcprof-otel-design.md
// §3.0 (wire format), §3.0.1 (Invariant T), §3.0.2 (causal-parent override).
const (
	// WcprofOpKindAttr (string) carries the wcprof op kind for a span when the
	// engine knows it, so the loader classifies the op without guessing —
	// e.g. "call_exec", "lazy", "service_start", "exec", "internal", "io". When
	// present it always wins over structural classification. (design §3.0, §5)
	WcprofOpKindAttr = "wcprof.op.kind"

	// WcprofWorkTypeAttr (string) coarsely attributes an op's self-time so
	// analysis can separate engine overhead from user workload and external
	// I/O: one of "engine", "user", "external". Absent ⇒ "engine". (design §3.3)
	WcprofWorkTypeAttr = "wcprof.work_type"

	// WcprofParentAttr (string) is an explicit *causal*-parent override for a
	// span whose parentId is deliberately a non-causal UI parent (the lazy
	// re-point, design §2.5/§3.2). Its value is the causal parent's OTel span
	// id encoded as the lower-hex string hex.EncodeToString(spanID[:]) — the
	// 16-char form spans/links use on the wire — so the stamping span processor
	// and the loader cannot diverge on encoding. The loader's causal parent is
	// WcprofParentAttr ?? parentId; it only ever *reads* this, never derives it.
	// (design §3.0.2)
	WcprofParentAttr = "wcprof.parent"

	// Wait-edge link attributes. A wait edge is emitted as a span link on the
	// *waiter*'s span carrying LinkPurposeAttr=LinkPurposeWait, plus these.
	// Timestamps are absolute Unix nanoseconds encoded as decimal strings: the
	// engine only knows wall-clock at emit time (the trace epoch is unknowable
	// until all spans are ingested, so the loader rebases), and decimal strings
	// round-trip exactly through Cloud's map[string]any JSON decode where a
	// number would be coerced to float64 and lose nanosecond precision above
	// 2^53. (design §3.0)
	//
	// WcprofWaitStartUnixNanoAttr / WcprofWaitEndUnixNanoAttr bound the blocked
	// interval (decimal-string absolute Unix nanos).
	WcprofWaitStartUnixNanoAttr = "wcprof.wait.start_unix_ns"
	WcprofWaitEndUnixNanoAttr   = "wcprof.wait.end_unix_ns"
	// WcprofWaitReasonAttr (string) names why the waiter blocked: one of
	// "singleflight", "call_exec", "lazy", "service", "lock", "exec", "io".
	WcprofWaitReasonAttr = "wcprof.wait.reason"
	// WcprofWaitIdentAttr (string) names the awaited resource for waits that
	// have no target span (reason "lock"), in place of the link's target span id.
	WcprofWaitIdentAttr = "wcprof.wait.ident"

	// LinkPurposeWait is a new value for telemetry.LinkPurposeAttr
	// ("dagger.io/link.purpose"), alongside the existing "cause"/"error_origin"
	// (defined in github.com/dagger/otel-go, which this repo cannot edit). It
	// marks a span link as a runtime wait edge for the wcprof analyzer. (design §3.0)
	LinkPurposeWait = "wait"
)
