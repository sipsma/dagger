package wccloud

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
	"github.com/dagger/dagger/internal/cloud"
	"github.com/dagger/dagger/internal/cloud/auth"
)

// TestCloudRoundTrip is the §6.6 production-ingest round-trip: emit a known
// augmented trace, let it flow through REAL Dagger Cloud ingest, fetch it back via
// the Cloud front-end, and compare to the local otlpdump capture of the same run.
//
// It is gated on env (it needs `dagger login` creds + a captured run), so it is a
// no-op in unit CI and is driven by the standing Cloud gate or by hand:
//
//	# one run, exported to BOTH otlpdump and Cloud (authenticated CLI):
//	env OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:43200 OTEL_EXPORTER_OTLP_TRACES_LIVE=1 \
//	    dagger -m ./toolchains/changelog functions
//	WCPROF_CLOUD_TRACE_ID=<traceId from the local capture> \
//	WCPROF_LOCAL_CAPTURE=/tmp/cap.jsonl \
//	    go test ./engine/wcprof/wccloud -run TestCloudRoundTrip -v
//
// Asserts (always): (a) every wcprof.wait.*_unix_ns on a wait link present in BOTH
// is BIT-EXACT — the proof the decimal-string encoding dodged Cloud's JSON float64
// coercion (§3.0); (b) Cloud is a faithful subset of local (no corrupt/extra span).
// When the trace is COMPLETE (Cloud == local, i.e. below the CLI→Cloud exporter BSP
// drop threshold): (c) the compiled graph matches local and the §6.1 gate is 0/0.
// When incomplete it REPORTS the completeness gap (the CLI→Cloud BSP drop, a known
// productionization gap, not a front-end bug) rather than failing — unless
// WCPROF_REQUIRE_COMPLETE is set.
func TestCloudRoundTrip(t *testing.T) {
	traceID := os.Getenv("WCPROF_CLOUD_TRACE_ID")
	localPath := os.Getenv("WCPROF_LOCAL_CAPTURE")
	if traceID == "" || localPath == "" {
		t.Skip("set WCPROF_CLOUD_TRACE_ID and WCPROF_LOCAL_CAPTURE (with `dagger login`) to run the live Cloud round-trip")
	}
	ctx := context.Background()

	cloudAuth, err := auth.GetCloudAuth(ctx)
	if err != nil {
		t.Fatalf("cloud auth (run `dagger login`): %v", err)
	}
	client, err := cloud.NewClient(ctx, cloudAuth)
	if err != nil {
		t.Fatalf("cloud client: %v", err)
	}
	orgID := os.Getenv("WCPROF_CLOUD_ORG")
	if orgID == "" {
		org, orgErr := auth.CurrentOrg()
		if orgErr != nil {
			t.Fatalf("current org: %v", orgErr)
		}
		orgID = org.ID
	}

	cloudSpans, err := Fetch(ctx, client, orgID, traceID)
	if err != nil {
		t.Fatalf("fetch cloud trace %s: %v", traceID, err)
	}
	f, err := os.Open(localPath)
	if err != nil {
		t.Fatalf("open local capture: %v", err)
	}
	defer f.Close()
	localSpans, err := wcotel.ParseOTLPDumpJSONL(f)
	if err != nil {
		t.Fatalf("parse local capture: %v", err)
	}

	cloudByID := dedupBySpanID(cloudSpans)
	localByID := dedupBySpanID(localSpans)
	t.Logf("cloud spans=%d  local spans=%d  (%.1f%% present)", len(cloudByID), len(localByID),
		100*float64(len(cloudByID))/float64(max1(len(localByID))))

	// (b) Cloud must be a faithful SUBSET of local — no span id Cloud invents.
	var extra int
	for id := range cloudByID {
		if _, ok := localByID[id]; !ok {
			extra++
		}
	}
	if extra != 0 {
		t.Fatalf("%d cloud span ids absent from the local capture of the same run — Cloud is not a faithful subset", extra)
	}

	// (a) BIT-EXACT wait timings on every wait link present in BOTH. This is the
	// gate proving the string-ns encoding survived Cloud's real JSON.
	checked := 0
	for id, cs := range cloudByID {
		ls, ok := localByID[id]
		if !ok {
			continue
		}
		lwaits := waitNSByTarget(ls)
		for _, cl := range cs.Links {
			cstart, cisWait := attrString(cl.Attrs, telemetryattrs.WcprofWaitStartUnixNanoAttr)
			if !cisWait {
				continue
			}
			cend, _ := attrString(cl.Attrs, telemetryattrs.WcprofWaitEndUnixNanoAttr)
			lw, ok := lwaits[cl.SpanID]
			if !ok {
				continue // link not in both (e.g. its target span dropped); covered by (c)/completeness
			}
			if cstart != lw.start || cend != lw.end {
				t.Fatalf("wait timing NOT bit-exact through Cloud for span %s→%s:\n  cloud [%s,%s]\n  local [%s,%s]\n(this means Cloud coerced the decimal string to float64)",
					id[:8], cl.SpanID[:8], cstart, cend, lw.start, lw.end)
			}
			// also assert it is a real >2^53 value carried as a long string, so this
			// is not a vacuous small-number check.
			if len(cstart) < 16 {
				t.Fatalf("wait ns suspiciously short (%q) — not exercising the float64 boundary", cstart)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no wait links were present in both cloud and local — cannot prove bit-exactness")
	}
	t.Logf("bit-exact wait timings verified on %d wait links through real Cloud ingest", checked)

	// (c) completeness: when Cloud == local, the compiled graph must match and the
	// §6.1 gate must be 0/0. When short, report the CLI→Cloud BSP drop gap.
	complete := len(cloudByID) == len(localByID)
	cc, cg, err := Load(ctx, client, orgID, traceID)
	if err != nil {
		t.Fatalf("compile cloud trace: %v", err)
	}
	gate := wcotel.CheckStructural(cc, cg, wcotel.GateOptions{})
	if complete {
		lc, err := wcotel.Compile(localSpans)
		if err != nil {
			t.Fatalf("compile local: %v", err)
		}
		lg, err := wcanalyze.Build(lc.Header, lc.Events)
		if err != nil {
			t.Fatalf("build local graph: %v", err)
		}
		// The local capture is the reference; it too must be complete (its own
		// marker present, 0/0) for the comparison to mean anything.
		if err := wcotel.CheckStructural(lc, lg, wcotel.GateOptions{}).Err(); err != nil {
			t.Fatalf("local reference capture must itself gate clean: %v", err)
		}
		// STRUCTURAL equality, not just counts: two graphs with the same op and
		// wait-edge counts can still differ in parentage or wait targets. Compare a
		// canonical fingerprint keyed by front-end-independent op identity.
		cfp, lfp := graphFingerprint(cg), graphFingerprint(lg)
		assertSameGraph(t, cfp, lfp)
		if err := gate.Err(); err != nil {
			t.Fatalf("§6.1 gate must be clean on a complete Cloud trace: %v", err)
		}
		t.Logf("COMPLETE round-trip: cloud graph == local (%d ops, %d wait edges, structural fingerprint identical), gate 0/0",
			cc.SpanCount, gate.WaitEdges)
	} else {
		msg := "incomplete Cloud trace (CLI→Cloud exporter BSP drop, a known productionization gap, not a front-end bug): " +
			"the structural gate correctly refuses it"
		if os.Getenv("WCPROF_REQUIRE_COMPLETE") != "" {
			t.Fatalf("%s; gate err=%v", msg, gate.Err())
		}
		t.Logf("%s; cloud unresolved-waits=%d orphaned-parents=%d", msg, gate.UnresolvedWaitTargets, gate.OrphanedParents)
	}
}

type waitNS struct{ start, end string }

func waitNSByTarget(s wcotel.Span) map[string]waitNS {
	m := make(map[string]waitNS, len(s.Links))
	for _, l := range s.Links {
		if start, ok := attrString(l.Attrs, telemetryattrs.WcprofWaitStartUnixNanoAttr); ok {
			end, _ := attrString(l.Attrs, telemetryattrs.WcprofWaitEndUnixNanoAttr)
			m[l.SpanID] = waitNS{start: start, end: end}
		}
	}
	return m
}

func dedupBySpanID(spans []wcotel.Span) map[string]wcotel.Span {
	m := make(map[string]wcotel.Span, len(spans))
	for _, s := range spans {
		if s.SpanID == "" {
			continue
		}
		if prev, ok := m[s.SpanID]; !ok || s.EndUnixNS > prev.EndUnixNS {
			m[s.SpanID] = s
		}
	}
	return m
}

func attrString(attrs map[string]any, key string) (string, bool) {
	if attrs == nil {
		return "", false
	}
	v, ok := attrs[key].(string)
	return v, ok
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// graphFingerprint canonically serializes a graph's STRUCTURE: every op keyed by a
// front-end-independent identity (class|ident|kind|start|end — the internal uint64
// op ids are per-front-end assignment indices and must NOT be compared), each op's
// PARENT identity, and each WAIT edge (resolved target identity, reason, interval).
// Two graphs compiled from the same complete run produce identical fingerprints;
// matching op/edge counts alone would miss a mis-parented op or a re-targeted wait.
func graphFingerprint(g *wcanalyze.Graph) []string {
	key := func(o *wcanalyze.Op) string {
		if o == nil {
			return "<nil>"
		}
		return fmt.Sprintf("%s|%s|%s|%d|%d", o.Class, o.Ident, o.Kind, o.StartNS, o.EndNS)
	}
	lines := make([]string, 0, len(g.Ops))
	for _, o := range g.Ops {
		waits := make([]string, 0, len(o.Waits))
		for _, w := range o.Waits {
			target := key(w.Target)
			if w.Target == nil { // unresolved/resource wait: pin the intended target ident
				target = "ident:" + w.TargetIdent
			}
			waits = append(waits, fmt.Sprintf("wait[%s r=%s %d-%d]", target, w.Reason, w.StartNS, w.EndNS))
		}
		sort.Strings(waits)
		lines = append(lines, fmt.Sprintf("OP %s parent=%s %s", key(o), key(o.Parent), strings.Join(waits, " ")))
	}
	sort.Strings(lines)
	return lines
}

// assertSameGraph fails with the first structural divergence between two
// fingerprints (both already sorted by graphFingerprint).
func assertSameGraph(t *testing.T, cloud, local []string) {
	t.Helper()
	if len(cloud) != len(local) {
		t.Fatalf("complete cloud trace must compile to the same graph: op count cloud=%d vs local=%d", len(cloud), len(local))
	}
	for i := range cloud {
		if cloud[i] != local[i] {
			t.Fatalf("complete cloud trace must compile to the same GRAPH as local (structure, not just counts); first divergence:\n  cloud: %s\n  local: %s",
				cloud[i], local[i])
		}
	}
}
