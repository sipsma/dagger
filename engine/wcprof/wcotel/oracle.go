package wcotel

// The cross-source oracle (design §6.2): the strongest faithfulness check. Run
// one workload on a dev engine with BOTH sources live — native wcprof
// (_DAGGER_WCPROF=1 / --profile) and the OTel augmentation — then compile both
// to the wcprof IR and compare their RunWhatIfs bottleneck rankings. Native is
// ground truth; the OTel source is faithful iff it produces the same top-N
// bottleneck classes with savings within tolerance. Drift localizes the next
// emit bug to a specific class (use wcanalyze.DriftOrigins).
//
// This file is the reusable harness (Chunks 2–5): LoadNativeDump pairs with the
// existing Load (OTel), and Oracle/CompareTopN do the ranking comparison. The
// two graphs MUST come from the same run to be comparable (impl-plan §1, §4.6).

import (
	"fmt"
	"io"
	"sort"

	"github.com/dagger/dagger/engine/wcprof"
	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

// LoadNativeDump reads a native wcprof dump (the /debug/wcprof/dump endpoint or
// --profile output) and builds the analyzer graph — the oracle's ground-truth
// source to compare the OTel source against.
func LoadNativeDump(r io.Reader) (*wcanalyze.Graph, error) {
	header, events, err := wcprof.ReadDump(r)
	if err != nil {
		return nil, err
	}
	g, err := wcanalyze.Build(header, events)
	if err != nil {
		return nil, fmt.Errorf("build native graph: %w", err)
	}
	return g, nil
}

// ClassImpact is one class's simulated bottleneck impact: the makespan saved by
// scaling its self-time by a factor (design §6.2 ranking-level check).
type ClassImpact struct {
	Key     wcanalyze.ClassKey
	SavedNS int64
}

// TopBottlenecks runs RunWhatIfs at a single factor and returns the classes with
// positive makespan savings, ranked descending — the bottleneck ordering the
// oracle compares across sources. minSelfNS filters out classes too cheap to
// matter (mirroring the report's own threshold).
func TopBottlenecks(g *wcanalyze.Graph, factor float64, minSelfNS int64) (baselineNS int64, ranked []ClassImpact, err error) {
	baselineNS, results, err := wcanalyze.RunWhatIfs(g, []float64{factor}, minSelfNS)
	if err != nil {
		return 0, nil, err
	}
	for _, r := range results {
		if saved := r.SavedNS[factor]; saved > 0 {
			ranked = append(ranked, ClassImpact{Key: r.Key, SavedNS: saved})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].SavedNS != ranked[j].SavedNS {
			return ranked[i].SavedNS > ranked[j].SavedNS
		}
		if ranked[i].Key.Kind != ranked[j].Key.Kind {
			return ranked[i].Key.Kind < ranked[j].Key.Kind
		}
		return ranked[i].Key.Class < ranked[j].Key.Class
	})
	return baselineNS, ranked, nil
}

// SharedImpact is a class that ranks in both sources, with each source's saving.
type SharedImpact struct {
	Key      wcanalyze.ClassKey
	NativeNS int64
	OTelNS   int64
}

// RelDrift is |native−otel| / max(native, otel): the relative disagreement in
// this class's simulated saving (0 = identical).
func (s SharedImpact) RelDrift() float64 {
	d := s.NativeNS - s.OTelNS
	if d < 0 {
		d = -d
	}
	den := max(s.NativeNS, s.OTelNS)
	if den == 0 {
		return 0
	}
	return float64(d) / float64(den)
}

// OracleComparison is the cross-source agreement between native (ground truth)
// and OTel bottleneck rankings, truncated to the top-N of each (design §6.2).
type OracleComparison struct {
	Factor         float64
	TopN           int
	NativeBaseline int64
	OTelBaseline   int64
	NativeTop      []ClassImpact
	OTelTop        []ClassImpact

	// Shared are classes appearing in both top-N lists; NativeOnly / OTelOnly
	// are classes that ranked in exactly one source (a faithfulness gap).
	Shared     []SharedImpact
	NativeOnly []wcanalyze.ClassKey
	OTelOnly   []wcanalyze.ClassKey
}

// Oracle compiles both sources' bottleneck rankings and compares their top-N
// (design §6.2). It is the one-call entry point for the harness.
func Oracle(native, otel *wcanalyze.Graph, factor float64, topN int, minSelfNS int64) (OracleComparison, error) {
	nb, nativeTop, err := TopBottlenecks(native, factor, minSelfNS)
	if err != nil {
		return OracleComparison{}, fmt.Errorf("native what-ifs: %w", err)
	}
	ob, otelTop, err := TopBottlenecks(otel, factor, minSelfNS)
	if err != nil {
		return OracleComparison{}, fmt.Errorf("otel what-ifs: %w", err)
	}
	cmp := CompareTopN(nativeTop, otelTop, topN)
	cmp.Factor = factor
	cmp.NativeBaseline = nb
	cmp.OTelBaseline = ob
	return cmp, nil
}

// CompareTopN truncates both rankings to topN and computes the shared classes
// (with each side's saving) and the per-source-only classes.
func CompareTopN(native, otel []ClassImpact, topN int) OracleComparison {
	nativeTop := truncate(native, topN)
	otelTop := truncate(otel, topN)

	otelByKey := make(map[wcanalyze.ClassKey]int64, len(otelTop))
	for _, c := range otelTop {
		otelByKey[c.Key] = c.SavedNS
	}
	nativeByKey := make(map[wcanalyze.ClassKey]bool, len(nativeTop))

	cmp := OracleComparison{TopN: topN, NativeTop: nativeTop, OTelTop: otelTop}
	for _, c := range nativeTop {
		nativeByKey[c.Key] = true
		if otelNS, ok := otelByKey[c.Key]; ok {
			cmp.Shared = append(cmp.Shared, SharedImpact{Key: c.Key, NativeNS: c.SavedNS, OTelNS: otelNS})
		} else {
			cmp.NativeOnly = append(cmp.NativeOnly, c.Key)
		}
	}
	for _, c := range otelTop {
		if !nativeByKey[c.Key] {
			cmp.OTelOnly = append(cmp.OTelOnly, c.Key)
		}
	}
	return cmp
}

// JaccardTopN is |shared| / |union of both top-N key sets|: 1.0 when the two
// rankings cover exactly the same classes, 0.0 when disjoint.
func (c OracleComparison) JaccardTopN() float64 {
	union := len(c.Shared) + len(c.NativeOnly) + len(c.OTelOnly)
	if union == 0 {
		return 1
	}
	return float64(len(c.Shared)) / float64(union)
}

// MaxRelDrift is the largest per-class relative saving disagreement over the
// shared classes (0 when no class is shared).
func (c OracleComparison) MaxRelDrift() float64 {
	var maxDrift float64
	for _, s := range c.Shared {
		if d := s.RelDrift(); d > maxDrift {
			maxDrift = d
		}
	}
	return maxDrift
}

// Agrees reports whether the two sources converge within tolerance: at least
// minJaccard of the top-N classes match, and every shared class's saving agrees
// within maxRelDrift (design §6.2 "within tolerance").
func (c OracleComparison) Agrees(minJaccard, maxRelDrift float64) bool {
	return c.JaccardTopN() >= minJaccard && c.MaxRelDrift() <= maxRelDrift
}

// Write renders a one-block human summary of the oracle comparison.
func (c OracleComparison) Write(w io.Writer) {
	fmt.Fprintf(w, "cross-source oracle (factor=%g, top-%d): jaccard=%.2f max-rel-drift=%.2f\n",
		c.Factor, c.TopN, c.JaccardTopN(), c.MaxRelDrift())
	fmt.Fprintf(w, "  baseline makespan: native=%dns otel=%dns\n", c.NativeBaseline, c.OTelBaseline)
	for _, s := range c.Shared {
		fmt.Fprintf(w, "  = %-10s %-28s native=%-12d otel=%-12d drift=%.2f\n",
			s.Key.Kind, s.Key.Class, s.NativeNS, s.OTelNS, s.RelDrift())
	}
	for _, k := range c.NativeOnly {
		fmt.Fprintf(w, "  - native-only %s %s\n", k.Kind, k.Class)
	}
	for _, k := range c.OTelOnly {
		fmt.Fprintf(w, "  + otel-only   %s %s\n", k.Kind, k.Class)
	}
}

func truncate(c []ClassImpact, n int) []ClassImpact {
	if n > 0 && len(c) > n {
		return c[:n]
	}
	return c
}
