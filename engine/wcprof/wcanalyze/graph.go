// Package wcanalyze reconstructs an operation graph from a wcprof dump and
// runs offline wall-clock bottleneck analysis over it: self-time accounting,
// a replay-based counterfactual simulator, and per-class what-if rankings.
package wcanalyze

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"sync"

	"github.com/dagger/dagger/engine/wcprof"
)

// Op is one reconstructed operation interval.
type Op struct {
	ID       uint64
	ParentID uint64
	Kind     string
	WorkType string
	Outcome  string
	Class    string
	Ident    string
	ClientID string
	ResultID uint64
	StartNS  int64
	EndNS    int64
	// Argv is the scrubbed, bounded user command for a container-exec op
	// (decoded from the dump's interned MetaID JSON-array string); nil for
	// non-exec ops or an exec with no resolved command. ClassifyExecs derives
	// the op's per-command Class from it. Never inferred from a span name.
	Argv []string
	// CacheInputs is a call op's cache-input recipe digests (the structural
	// inputs the cache computed for its term lookup), decoded from the dump's
	// interned InputsID JSON-array string — the cache-DAG edges both sources
	// now carry (native emit; OTel dag.inputs). nil when not recorded.
	CacheInputs []string
	// Open marks ops that had not ended at dump time; EndNS is the dump time.
	Open bool

	Parent   *Op
	Children []*Op // sorted by StartNS
	Waits    []*WaitEdge
	// Reparented marks ops whose parent was assigned via a nested-client
	// link rather than a recorded parent ID.
	Reparented bool

	// selfSegments is the op interval minus child intervals and waits,
	// computed lazily by the analysis.
	selfSegments []segment
}

func (op *Op) Duration() int64 {
	return op.EndNS - op.StartNS
}

// WaitEdge is one recorded blocked-on interval.
type WaitEdge struct {
	Waiter      *Op // nil if the waiting code had no profiled op
	Target      *Op // nil for unresolved or resource waits
	TargetIdent string
	Reason      string
	StartNS     int64
	EndNS       int64
}

func (w *WaitEdge) Duration() int64 {
	return w.EndNS - w.StartNS
}

// ForcedEdge is one recorded forced-evaluation fact (lazy-semantics §4.4):
// Forcer demanded an already-complete lazy result of the producer recipe
// digest Ident. Zero duration; never gates the replay — consumed only by the
// what-if-cached keep test.
type ForcedEdge struct {
	Forcer *Op
	// Target is the completing lazy op when the run recorded it; nil when
	// production predated recording (the Ident still carries the fact).
	Target *Op
	Ident  string
}

// Graph is the reconstructed op graph for one dump.
type Graph struct {
	Ops   map[uint64]*Op
	Roots []*Op // ops with no (resolved) parent, sorted by StartNS

	// OrphanWaits are waits whose waiter op is unknown.
	OrphanWaits []*WaitEdge

	// ForcedEdges are the recorded forced-evaluation facts, in event order.
	// Forcer is always known here; a fact whose forcer op is unknown lands
	// in OrphanForcedFacts instead — like an orphan wait it can demand
	// nothing (no liveness to test), but it is retained and counted, never
	// silently dropped.
	ForcedEdges       []*ForcedEdge
	OrphanForcedFacts []*ForcedEdge

	DroppedEvents uint64
	OpenOps       int

	// Emit-side suppression counters from the dump header (0 on captures
	// predating them). Derivation failures make the what-if-cached analysis
	// REFUSE the capture (a suppressed ident/fact could alter its answer);
	// uninstrumented forcers are a declared model boundary printed as a
	// caveat. The OTel loader fills the derivation count from per-firing
	// suppression links (exact; loss shows as dropped links, gated).
	SuppressedIdentDerivations      uint64
	SuppressedUninstrumentedForcers uint64

	// TraceStartNS/TraceEndNS bound all recorded activity.
	TraceStartNS int64
	TraceEndNS   int64

	// ResultIDsCaptureLocal marks graphs whose Op.ResultID values are
	// per-capture interns (the OTel loader interns dag.output strings)
	// rather than the engine's global shared-result ids. Such ids are never
	// comparable across captures, so the calibration's cross-capture
	// result-id consumptions (design §3.7) are disabled for them.
	ResultIDsCaptureLocal bool

	// prog is the compiled replay program, built once on first simulation.
	progOnce sync.Once
	prog     *replayProgram

	// cachedIdx is the structural index for what-if-cached hypothesis
	// resolution (cached.go), built once on first use. It depends only on the
	// nesting/wait structure (never on op classes), so it survives
	// invalidateProgram; its dense op indexing is the same deterministic
	// ID-sorted order every program compilation produces.
	cachedIdxOnce sync.Once
	cachedIdx     *cachedIndex
}

// ClassKey identifies an operation class for aggregation: ops are grouped by
// kind+class (e.g. call_exec / "Container.withExec").
type ClassKey struct {
	Kind  string
	Class string
}

func (k ClassKey) String() string {
	return k.Kind + ":" + k.Class
}

// Load reads a wcprof dump and reconstructs the op graph.
func Load(r io.Reader) (*Graph, error) {
	header, events, err := wcprof.ReadDump(r)
	if err != nil {
		return nil, err
	}
	return Build(header, events)
}

// LoadMulti reads multiple dumps taken from the same recorder (periodic
// drains of one engine run) and reconstructs one combined op graph.
//
// This relies on recorder guarantees across flushes: the string table only
// grows (IDs are stable), op IDs are globally unique, the epoch is fixed,
// and the dropped-event counter is cumulative. The merge keeps the header
// with the longest string table and latest open-ops view, and concatenates
// all events.
func LoadMulti(readers []io.Reader) (*Graph, error) {
	if len(readers) == 0 {
		return nil, fmt.Errorf("no dumps to load")
	}
	var (
		merged    *wcprof.DumpHeader
		allEvents []wcprof.DumpEvent
	)
	for i, r := range readers {
		header, events, err := wcprof.ReadDump(r)
		if err != nil {
			return nil, fmt.Errorf("dump %d: %w", i, err)
		}
		if merged == nil {
			merged = header
		} else {
			if header.EpochUnixNano != merged.EpochUnixNano {
				return nil, fmt.Errorf("dump %d: epoch mismatch (%d != %d): dumps are from different recorder runs", i, header.EpochUnixNano, merged.EpochUnixNano)
			}
			if len(header.Strings) >= len(merged.Strings) {
				merged.Strings = header.Strings
			}
			if header.DumpedUnixNano >= merged.DumpedUnixNano {
				merged.DumpedUnixNano = header.DumpedUnixNano
				merged.OpenOps = header.OpenOps
			}
			merged.DroppedEvents = max(merged.DroppedEvents, header.DroppedEvents)
			merged.SuppressedIdentDerivations = max(merged.SuppressedIdentDerivations, header.SuppressedIdentDerivations)
			merged.SuppressedUninstrumentedForcers = max(merged.SuppressedUninstrumentedForcers, header.SuppressedUninstrumentedForcers)
		}
		allEvents = append(allEvents, events...)
	}
	merged.EventCount = len(allEvents)
	return Build(merged, allEvents)
}

// decodeArgv recovers a user-exec op's argv from its interned MetaID string (the
// canonical scalar JSON-array encoding both sources emit). Empty ⇒ nil (the op
// stays the aggregated exec blob); a malformed string ⇒ nil too — defensive, never
// a panic, and never inferred from anything but the explicit emitted value.
func decodeArgv(s string) []string {
	if s == "" {
		return nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(s), &argv); err != nil {
		return nil
	}
	return argv
}

// Build reconstructs the op graph from parsed dump data.
//
//nolint:gocyclo // linear reconstruction flow over the event union
func Build(header *wcprof.DumpHeader, events []wcprof.DumpEvent) (*Graph, error) {
	str := func(id uint32) string {
		if int(id) >= len(header.Strings) {
			return fmt.Sprintf("<bad-string-%d>", id)
		}
		return header.Strings[id]
	}

	g := &Graph{
		Ops:                             make(map[uint64]*Op),
		DroppedEvents:                   header.DroppedEvents,
		SuppressedIdentDerivations:      header.SuppressedIdentDerivations,
		SuppressedUninstrumentedForcers: header.SuppressedUninstrumentedForcers,
	}

	dumpRelNS := header.DumpedUnixNano - header.EpochUnixNano

	type rawWait struct {
		waiterID uint64
		targetID uint64
		ident    string
		reason   string
		startNS  int64
		endNS    int64
	}
	type rawLink struct {
		fromID   uint64
		targetID uint64
		ident    string
		kind     string
		resultID uint64
	}
	var waits []rawWait
	var links []rawLink

	for _, ev := range events {
		switch ev.Type {
		case "op":
			g.Ops[ev.OpID] = &Op{
				ID:          ev.OpID,
				ParentID:    ev.ParentID,
				Kind:        ev.OpKind,
				WorkType:    ev.WorkType,
				Outcome:     ev.Outcome,
				Class:       str(ev.ClassID),
				Ident:       str(ev.IdentID),
				ClientID:    str(ev.ClientID),
				ResultID:    ev.ResultID,
				Argv:        decodeArgv(str(ev.MetaID)),
				CacheInputs: decodeArgv(str(ev.InputsID)),
				StartNS:     ev.StartNS,
				EndNS:       max(ev.EndNS, ev.StartNS),
			}
		case "wait":
			waits = append(waits, rawWait{
				waiterID: ev.ParentID,
				targetID: ev.TargetID,
				ident:    str(ev.IdentID),
				reason:   ev.Reason,
				startNS:  ev.StartNS,
				endNS:    max(ev.EndNS, ev.StartNS),
			})
		case "link":
			links = append(links, rawLink{
				fromID:   ev.ParentID,
				targetID: ev.TargetID,
				ident:    str(ev.IdentID),
				kind:     ev.LinkKind,
				resultID: ev.ResultID,
			})
		}
	}

	// Ops still open at dump time get the dump timestamp as their end.
	for _, oo := range header.OpenOps {
		if _, exists := g.Ops[oo.OpID]; exists {
			continue
		}
		g.Ops[oo.OpID] = &Op{
			ID:       oo.OpID,
			ParentID: oo.ParentID,
			Kind:     oo.Kind,
			WorkType: oo.WorkType,
			Class:    str(oo.ClassID),
			Ident:    str(oo.IdentID),
			ClientID: str(oo.ClientID),
			Argv:     decodeArgv(str(oo.MetaID)),
			StartNS:  oo.StartNS,
			EndNS:    max(dumpRelNS, oo.StartNS),
			Open:     true,
		}
		g.OpenOps++
	}

	// Index exec ops by ident so exec-reason ident waits can resolve to them.
	execByIdent := make(map[string]*Op)
	for _, op := range g.Ops {
		if op.Kind == wcprof.OpKindExec.String() && op.Ident != "" {
			// prefer the longest-running exec for an ident if duplicated
			if cur, ok := execByIdent[op.Ident]; !ok || op.Duration() > cur.Duration() {
				execByIdent[op.Ident] = op
			}
		}
	}

	// Attach waits.
	for _, rw := range waits {
		w := &WaitEdge{
			TargetIdent: rw.ident,
			Reason:      rw.reason,
			StartNS:     rw.startNS,
			EndNS:       rw.endNS,
		}
		if t, ok := g.Ops[rw.targetID]; ok {
			w.Target = t
		} else if rw.reason == "exec" && rw.ident != "" {
			if t, ok := execByIdent[rw.ident]; ok {
				w.Target = t
			}
		}
		if waiter, ok := g.Ops[rw.waiterID]; ok {
			w.Waiter = waiter
			waiter.Waits = append(waiter.Waits, w)
		} else {
			g.OrphanWaits = append(g.OrphanWaits, w)
		}
	}

	// Nested-client links: clientID -> hosting exec op. Forced-evaluation
	// links become first-class edges (a nil target is legitimate there:
	// production predated recording).
	nestedClientExec := make(map[string]*Op)
	for _, rl := range links {
		if rl.kind == "forced" {
			fe := &ForcedEdge{Ident: rl.ident}
			if t, ok := g.Ops[rl.targetID]; ok {
				fe.Target = t
			}
			if forcer, ok := g.Ops[rl.fromID]; ok {
				fe.Forcer = forcer
				g.ForcedEdges = append(g.ForcedEdges, fe)
			} else {
				g.OrphanForcedFacts = append(g.OrphanForcedFacts, fe)
			}
			continue
		}
		if rl.kind != "nested_client" || rl.ident == "" {
			continue
		}
		if from, ok := g.Ops[rl.fromID]; ok {
			nestedClientExec[rl.ident] = from
		}
	}

	// Wire parents. Roots belonging to a nested client get re-parented under
	// the exec op hosting that client.
	for _, op := range g.Ops {
		if op.ParentID != 0 {
			if parent, ok := g.Ops[op.ParentID]; ok && parent != op {
				op.Parent = parent
				continue
			}
		}
		if hostExec, ok := nestedClientExec[op.ClientID]; ok && hostExec != op {
			op.Parent = hostExec
			op.Reparented = true
		}
	}
	for _, op := range g.Ops {
		if op.Parent != nil {
			op.Parent.Children = append(op.Parent.Children, op)
		} else {
			g.Roots = append(g.Roots, op)
		}
	}
	for _, op := range g.Ops {
		slices.SortFunc(op.Children, func(a, b *Op) int {
			if a.StartNS != b.StartNS {
				return int(a.StartNS - b.StartNS)
			}
			return int(a.ID - b.ID)
		})
		slices.SortFunc(op.Waits, func(a, b *WaitEdge) int {
			return int(a.StartNS - b.StartNS)
		})
	}
	slices.SortFunc(g.Roots, func(a, b *Op) int {
		if a.StartNS != b.StartNS {
			return int(a.StartNS - b.StartNS)
		}
		return int(a.ID - b.ID)
	})

	first := true
	for _, op := range g.Ops {
		if first {
			g.TraceStartNS, g.TraceEndNS = op.StartNS, op.EndNS
			first = false
			continue
		}
		g.TraceStartNS = min(g.TraceStartNS, op.StartNS)
		g.TraceEndNS = max(g.TraceEndNS, op.EndNS)
	}

	return g, nil
}

// segment is a half-open interval [Start, End).
type segment struct {
	Start, End int64
}

// subtractIntervals returns base minus the union of cuts (cuts may overlap
// and extend beyond base).
func subtractIntervals(base segment, cuts []segment) []segment {
	if len(cuts) == 0 {
		if base.End > base.Start {
			return []segment{base}
		}
		return nil
	}
	sorted := slices.Clone(cuts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	var out []segment
	cursor := base.Start
	for _, cut := range sorted {
		if cut.End <= cursor || cut.Start >= base.End {
			continue
		}
		if cut.Start > cursor {
			out = append(out, segment{cursor, min(cut.Start, base.End)})
		}
		cursor = max(cursor, cut.End)
		if cursor >= base.End {
			return out
		}
	}
	if cursor < base.End {
		out = append(out, segment{cursor, base.End})
	}
	return out
}

func sumSegments(segs []segment) int64 {
	var total int64
	for _, s := range segs {
		total += s.End - s.Start
	}
	return total
}

// SelfSegments returns the op's interval minus its children's intervals and
// its own wait intervals: the time the op was plausibly doing its own work.
func (op *Op) SelfSegments() []segment {
	if op.selfSegments != nil {
		return op.selfSegments
	}
	cuts := make([]segment, 0, len(op.Children)+len(op.Waits))
	for _, c := range op.Children {
		cuts = append(cuts, segment{c.StartNS, c.EndNS})
	}
	for _, w := range op.Waits {
		cuts = append(cuts, segment{w.StartNS, w.EndNS})
	}
	segs := subtractIntervals(segment{op.StartNS, op.EndNS}, cuts)
	if segs == nil {
		segs = []segment{}
	}
	op.selfSegments = segs
	return segs
}

// SelfNS is the total self time of the op.
func (op *Op) SelfNS() int64 {
	return sumSegments(op.SelfSegments())
}

// Key returns the op's aggregation class key.
func (op *Op) Key() ClassKey {
	return ClassKey{Kind: op.Kind, Class: op.Class}
}

// invalidateProgram resets the memoized replay program so the next simulation
// recompiles its class buckets from the current op.Class values. ClassifyExecs
// calls it after relabeling exec ops, so a program already compiled (e.g. by the
// OTel structural gate) cannot leave the what-if savings computed on the stale
// pre-classify class table while the report re-buckets live. This is a cache
// reset on the Graph, NOT a change to the replay algorithm.
func (g *Graph) invalidateProgram() {
	g.progOnce = sync.Once{}
	g.prog = nil
}
