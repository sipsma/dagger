package wcprof

import (
	"encoding/json"
	"strconv"
	"strings"
)

// The E1 lookup-outcome vocabulary and the E2 scope-input encoding
// (cache-invalidation-tracing design §4). Both are capture-schema additions:
// small canonical strings interned once per distinct value, decoded by the
// one shared analyzer Build path so native dumps and OTel traces carry
// byte-identical facts.

// Lookup entries: which cache-lookup entry point produced the fact. The
// entry is orthogonal metadata, not a reason (design §4 E1, round-3
// correction) — specific reasons apply on both entries.
const (
	// LookupEntryRequest is the ordinary call path
	// (Cache.getOrInitCallInner → lookupCacheForRequest).
	LookupEntryRequest = "request"
	// LookupEntryDigestOnly is the ID/recipe-loading path
	// (Cache.lookupCacheForDigests), which has no call op of its own — its
	// facts ride as LinkKindLookupOutcome events.
	LookupEntryDigestOnly = "digest_only"
)

// Lookup reasons: why a performed lookup returned no usable hit — exactly
// one per such lookup, precedence = the first terminal reached on the
// engine's decision path (design §2):
//
//	expired            — candidate collection skipped TTL-expired results and
//	                     none remained (the earliest silent-drop terminal,
//	                     now counted instead of silent);
//	input_unknown      — the structural-term lookup aborted at input #k, the
//	                     first input digest the cache has never seen (the
//	                     walk's authoritative next hop);
//	no_live_candidate  — the structural term matched but none of its results
//	                     remain (released/collected in-run);
//	no_matching_term   — nothing matched at all;
//	session_filtered   — semantically-equivalent candidates exist but none
//	                     satisfies this session's resource requirements;
//	persisted_load_failed — a SELECTED hit's persisted payload failed to
//	                     load (the hit-unusable arm, not a miss reason).
const (
	LookupReasonExpired             = "expired"
	LookupReasonInputUnknown        = "input_unknown"
	LookupReasonNoLiveCandidate     = "no_live_candidate"
	LookupReasonNoMatchingTerm      = "no_matching_term"
	LookupReasonSessionFiltered     = "session_filtered"
	LookupReasonPersistedLoadFailed = "persisted_load_failed"
)

// EncodeLookupOutcome renders the canonical lookup-outcome string:
// "<entry> <reason>" plus " <inputIdx>" for input_unknown (0-based index
// into the call's structural input vector). Both sources emit this exact
// encoding; the analyzer decodes it with DecodeLookupOutcome.
func EncodeLookupOutcome(entry, reason string, inputIdx int) string {
	if reason == LookupReasonInputUnknown && inputIdx >= 0 {
		return entry + " " + reason + " " + strconv.Itoa(inputIdx)
	}
	return entry + " " + reason
}

// DecodeLookupOutcome parses EncodeLookupOutcome's encoding. ok is false for
// malformed strings — including input_unknown WITHOUT its required index, or
// an index on any other reason (review round 1: a malformed fact must never
// become accepted evidence); inputIdx is -1 when absent.
func DecodeLookupOutcome(s string) (entry, reason string, inputIdx int, ok bool) {
	parts := strings.Split(s, " ")
	if len(parts) < 2 || len(parts) > 3 || parts[0] == "" || parts[1] == "" {
		return "", "", -1, false
	}
	inputIdx = -1
	if len(parts) == 3 {
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 0 {
			return "", "", -1, false
		}
		inputIdx = n
	}
	if (parts[1] == LookupReasonInputUnknown) != (inputIdx >= 0) {
		return "", "", -1, false
	}
	return parts[0], parts[1], inputIdx, true
}

// ScopeInput is one scope implicit input on a call: an engine-computed
// input hashed into the recipe digest beyond the explicit arguments (dagql
// ImplicitInput — deliberate cache-key scoping). EmptyValue marks an input
// whose resolved value was the empty string: the engine deliberately NOT
// scoping on that path (e.g. container.from's fromSessionScope on a
// digest-pinned ref), which classification must not read as active scoping.
// Values themselves are never recorded — the names plus the emptiness flag
// are the deciding data. The JSON tags are the wire encoding shared with
// the OTel loader's dag.call parse; wcanalyze decodes both identically.
type ScopeInput struct {
	Name       string `json:"n"`
	EmptyValue bool   `json:"e,omitempty"`
}

// EncodeScopeInputs renders the canonical JSON array for interning ("[]"
// for a call with no scope inputs — an authoritative absence, distinct
// from not-recorded). Returns "" on a marshal error (never a partial emit).
func EncodeScopeInputs(inputs []ScopeInput) string {
	if inputs == nil {
		inputs = []ScopeInput{}
	}
	b, err := json.Marshal(inputs)
	if err != nil {
		return ""
	}
	return string(b)
}

// CallSelf is the canonical SELF structure of a call, parsed by the OTel
// loader from the recorded dag.call payload (E3b — loader work, no emit;
// invalidation-tracing design §4). It carries everything the self digest
// consumes, rendered to bounded strings, so pair mode can attribute a
// changed call at ARG level ("scalar arg 'platform' differed") instead of
// digest granularity. Native captures never carry it (a full native
// call-structure emit is REFUSED on volume grounds, stated in the design).
type CallSelf struct {
	Field    string      `json:"f"`
	Receiver string      `json:"r,omitempty"` // receiver call digest
	View     string      `json:"v,omitempty"`
	Nth      int64       `json:"n,omitempty"`
	Module   *CallModule `json:"m,omitempty"`
	Args     []CallArg   `json:"a,omitempty"`
	Implicit []CallArg   `json:"i,omitempty"`
}

// CallModule is the module providing a call's implementation.
type CallModule struct {
	CallDigest string `json:"d,omitempty"`
	Name       string `json:"n,omitempty"`
	Ref        string `json:"r,omitempty"`
	Pin        string `json:"p,omitempty"`
}

// CallArg is one named argument (or implicit input) with a bounded
// canonical rendering of its literal value (redactions appear as the
// recorded "***"; call references render as their digests).
type CallArg struct {
	Name  string `json:"n"`
	Value string `json:"v"`
}

// EncodeCallSelf renders the canonical JSON for interning; "" on marshal
// error (never a partial emit).
func EncodeCallSelf(cs *CallSelf) string {
	if cs == nil {
		return ""
	}
	b, err := json.Marshal(cs)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeCallSelf parses EncodeCallSelf's encoding; nil for empty or
// malformed strings (never guessed).
func DecodeCallSelf(s string) *CallSelf {
	if s == "" {
		return nil
	}
	cs := &CallSelf{}
	if err := json.Unmarshal([]byte(s), cs); err != nil {
		return nil
	}
	return cs
}
