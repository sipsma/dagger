package wcprof

import "testing"

// The E1 canonical encoding round-trips, and malformed strings are rejected
// rather than becoming accepted evidence (review round 1: input_unknown
// REQUIRES its index; an index on any other reason is equally malformed).
func TestLookupOutcomeEncoding(t *testing.T) {
	enc := EncodeLookupOutcome(LookupEntryRequest, LookupReasonInputUnknown, 2)
	entry, reason, idx, ok := DecodeLookupOutcome(enc)
	if !ok || entry != LookupEntryRequest || reason != LookupReasonInputUnknown || idx != 2 {
		t.Fatalf("round-trip = %q/%q/%d/%v", entry, reason, idx, ok)
	}
	enc = EncodeLookupOutcome(LookupEntryDigestOnly, LookupReasonExpired, -1)
	entry, reason, idx, ok = DecodeLookupOutcome(enc)
	if !ok || entry != LookupEntryDigestOnly || reason != LookupReasonExpired || idx != -1 {
		t.Fatalf("round-trip = %q/%q/%d/%v", entry, reason, idx, ok)
	}

	for _, bad := range []string{
		"",
		"request",
		"request input_unknown",    // the index is REQUIRED
		"request expired 3",        // an index on a non-input_unknown reason
		"request input_unknown -1", // negative index
		"request input_unknown x",  // non-numeric index
		" request expired",         // empty first field
		"request expired 1 2",      // too many fields
	} {
		if _, _, _, ok := DecodeLookupOutcome(bad); ok {
			t.Fatalf("malformed %q must be rejected", bad)
		}
	}
}
