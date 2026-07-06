// Package wccloud is the Dagger Cloud front-end for the wcprof OTel source. It
// fetches a trace from the Cloud trace API (internal/cloud) and converts it into
// the loader's Span IR, so the UNCHANGED wcotel.Compile / wcanalyze.Build stage
// analyzes a Cloud trace identically to a local otlpdump capture. Per the design
// (§5) Chunk 5 swaps only the loader's INPUT: this is a new front-end alongside
// otlpdump (kept for the dev loop), not a replacement, and it adds zero causal
// inference — it is a pure mechanical field map.
package wccloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/dagger/dagger/engine/wcprof/wcanalyze"
	"github.com/dagger/dagger/engine/wcprof/wcotel"
	"github.com/dagger/dagger/internal/cloud"
)

// statusCodeError is the OTLP proto status enum name Cloud returns for an errored
// span (cloud.SpanStatus.Code), mirroring the otlpdump loader's isErrorStatus.
const statusCodeError = "STATUS_CODE_ERROR"

// SpanFromCloud converts one Cloud SpanData into the loader's Span IR. It is a
// pure field map — the same translation ParseOTLPDumpJSONL does for the otlpdump
// wire shape — so the Cloud path introduces no causal inference (design §5).
//
// Ids are lower-cased so span/parent/link ids match each other and the otlpdump
// convention regardless of how Cloud cases its hex. The wait-edge timestamps ride
// in link attributes as decimal STRINGS (design §3.0): Cloud returns attributes as
// map[string]any, so a string attr decodes back to a Go string and parses exactly
// — the whole point of the string-ns encoding, asserted bit-exact by the §6.6
// round-trip test. Cloud cannot self-report per-span/per-link dropped counts, so
// DroppedLinks/DroppedAttrs stay 0; the link cap is engineered out via the
// engine's LinkCountLimit instead (design §6.1, §6.6).
func SpanFromCloud(s *cloud.SpanData) wcotel.Span {
	var parentID string
	if s.ParentID != nil {
		parentID = strings.ToLower(*s.ParentID)
	}
	var endNS uint64
	if s.EndTime != nil {
		endNS = uint64(s.EndTime.UnixNano())
	}
	links := make([]wcotel.Link, 0, len(s.Links))
	for i := range s.Links {
		links = append(links, wcotel.Link{
			SpanID: strings.ToLower(s.Links[i].SpanID),
			Attrs:  s.Links[i].Attributes,
		})
	}
	return wcotel.Span{
		TraceID:     strings.ToLower(s.TraceID),
		SpanID:      strings.ToLower(s.ID),
		ParentID:    parentID,
		Name:        s.Name,
		StartUnixNS: uint64(s.Timestamp.UnixNano()),
		EndUnixNS:   endNS,
		Attrs:       s.Attributes,
		StatusError: s.Status.Code == statusCodeError,
		Links:       links,
	}
}

// SpansFromCloud converts a batch of Cloud spans to the loader Span IR.
func SpansFromCloud(spans []cloud.SpanData) []wcotel.Span {
	out := make([]wcotel.Span, 0, len(spans))
	for i := range spans {
		out = append(out, SpanFromCloud(&spans[i]))
	}
	return out
}

// SpanStreamer is the subset of *cloud.Client the front-end needs. Narrowing it to
// an interface keeps wccloud testable with a fake (no live Cloud) and documents the
// exact dependency on the Cloud read surface.
type SpanStreamer interface {
	StreamSpans(ctx context.Context, orgID, traceID string, handler func([]cloud.SpanData)) error
}

// Fetch streams every span for traceID from Dagger Cloud and converts them to the
// loader Span IR. Cloud's spansUpdated(root:true, listen:nil) does a FULL store
// read below 100k rows (the incremental listen protocol is the >=100k branch), so
// for a realistic (<100k-span) trace this returns the complete trace; the prior
// "Cloud returns ~1/5" was the CLI→Cloud exporter BSP drop, which the skip fix
// removed (forensics). wcotel.Compile dedups any live start/end duplicates (keep
// the ended copy) and rejects a multi-trace set, so no dedup is needed here.
func Fetch(ctx context.Context, client SpanStreamer, orgID, traceID string) ([]wcotel.Span, error) {
	if client == nil {
		return nil, fmt.Errorf("wccloud: nil span streamer")
	}
	var all []cloud.SpanData
	if err := client.StreamSpans(ctx, orgID, traceID, func(batch []cloud.SpanData) {
		all = append(all, batch...)
	}); err != nil {
		return nil, fmt.Errorf("stream spans for trace %s: %w", traceID, err)
	}
	return SpansFromCloud(all), nil
}

// Load fetches, compiles, and builds the analyzer graph for a Cloud trace — the
// Cloud analog of wcotel.Load (which reads otlpdump JSONL). The compile/replay
// stage is byte-for-byte identical; only the input source differs (design §5).
func Load(ctx context.Context, client SpanStreamer, orgID, traceID string) (*wcotel.Compiled, *wcanalyze.Graph, error) {
	spans, err := Fetch(ctx, client, orgID, traceID)
	if err != nil {
		return nil, nil, err
	}
	c, err := wcotel.Compile(spans)
	if err != nil {
		return nil, nil, err
	}
	g, err := wcanalyze.Build(c.Header, c.Events)
	if err != nil {
		return nil, nil, fmt.Errorf("build graph: %w", err)
	}
	// Same OTel-source semantics as wcotel.Load: result ids are per-capture
	// interns of dag.output (never cross-capture comparable), and input
	// vectors are ordered ONLY where the E3a attr recorded them. Without
	// this marker a Cloud graph would pass for native — the calibration's
	// rid consumptions and pair mode's positional-pairing soundness gate
	// both key on it.
	g.ResultIDsCaptureLocal = true
	return c, g, nil
}
