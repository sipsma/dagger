package wcotel

import (
	"testing"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
)

func ck(kind, class string) wcanalyze.ClassKey {
	return wcanalyze.ClassKey{Kind: kind, Class: class}
}

// TestOracleCompareTopN covers the harness's ranking-comparison math (design
// §6.2) on its own, so the singleflight fixtures test the emit shape and this
// tests the comparison: shared classes, per-source-only classes, jaccard, and
// per-class relative drift.
func TestOracleCompareTopN(t *testing.T) {
	native := []ClassImpact{
		{Key: ck("call_exec", "A"), SavedNS: 100},
		{Key: ck("exec", "B"), SavedNS: 50},
		{Key: ck("call", "C"), SavedNS: 10}, // native-only within top-3
	}
	otel := []ClassImpact{
		{Key: ck("call_exec", "A"), SavedNS: 96}, // 4% drift vs native
		{Key: ck("exec", "B"), SavedNS: 50},      // exact
		{Key: ck("internal", "D"), SavedNS: 12},  // otel-only within top-3
	}

	cmp := CompareTopN(native, otel, 3)
	if len(cmp.Shared) != 2 {
		t.Fatalf("want 2 shared classes (A,B), got %d", len(cmp.Shared))
	}
	if len(cmp.NativeOnly) != 1 || cmp.NativeOnly[0] != ck("call", "C") {
		t.Fatalf("want C native-only, got %v", cmp.NativeOnly)
	}
	if len(cmp.OTelOnly) != 1 || cmp.OTelOnly[0] != ck("internal", "D") {
		t.Fatalf("want D otel-only, got %v", cmp.OTelOnly)
	}
	// Jaccard = |shared| / |union| = 2 / 4 = 0.5.
	if j := cmp.JaccardTopN(); j != 0.5 {
		t.Fatalf("jaccard: want 0.5, got %.3f", j)
	}
	// Max relative drift is on class A: |100-96|/100 = 0.04.
	if d := cmp.MaxRelDrift(); d < 0.039 || d > 0.041 {
		t.Fatalf("max-rel-drift: want ~0.04, got %.4f", d)
	}
	// Agrees thresholds: passes a lenient bar, fails a strict jaccard bar.
	if !cmp.Agrees(0.5, 0.05) {
		t.Fatal("should agree at jaccard>=0.5, drift<=0.05")
	}
	if cmp.Agrees(0.9, 0.05) {
		t.Fatal("should not agree at jaccard>=0.9 (only 0.5 overlap)")
	}
	if cmp.Agrees(0.5, 0.03) {
		t.Fatal("should not agree at drift<=0.03 (A drifts 0.04)")
	}
}

// TestOracleTruncationAndDisjoint: empty intersection scores jaccard 0, and
// top-N truncation is applied before comparison.
func TestOracleTruncationAndDisjoint(t *testing.T) {
	native := []ClassImpact{{Key: ck("call_exec", "A"), SavedNS: 100}}
	otel := []ClassImpact{{Key: ck("call_exec", "Z"), SavedNS: 100}}
	cmp := CompareTopN(native, otel, 5)
	if cmp.JaccardTopN() != 0 {
		t.Fatalf("disjoint rankings must score jaccard 0, got %.2f", cmp.JaccardTopN())
	}
	if cmp.Agrees(0.01, 1.0) {
		t.Fatal("disjoint rankings must not agree")
	}

	// Truncation: a class outside top-1 is excluded from the comparison.
	native2 := []ClassImpact{
		{Key: ck("call_exec", "A"), SavedNS: 100},
		{Key: ck("exec", "B"), SavedNS: 50},
	}
	otel2 := []ClassImpact{
		{Key: ck("call_exec", "A"), SavedNS: 100},
		{Key: ck("exec", "B"), SavedNS: 50},
	}
	if c := CompareTopN(native2, otel2, 1); len(c.Shared) != 1 || c.JaccardTopN() != 1 {
		t.Fatalf("top-1 truncation: want 1 shared at jaccard 1, got shared=%d jaccard=%.2f", len(c.Shared), c.JaccardTopN())
	}
}
