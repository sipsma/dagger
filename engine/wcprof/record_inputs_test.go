package wcprof

import (
	"bytes"
	"context"
	"testing"
)

// TestSetInputsInternsInputsID: SetInputs marshals a call's cache-input
// digests to the canonical scalar JSON-array string and interns it as the
// op's InputsID, round-tripped through the dump (the cache-DAG edge seam,
// what-if-cached design Chunk 4).
func TestSetInputsInternsInputsID(t *testing.T) {
	r := withTestRecorder(t, 0)
	ctx := context.Background()

	_, op := BeginOp(ctx, OpKindCall, "Container.withExec", OpOpts{})
	op.SetIdent("xxh3:call-digest")
	op.SetInputs([]string{"xxh3:input-a", "xxh3:input-b"})
	op.End(OutcomeExecuted)

	// An input-less call interns no InputsID (0), staying absent downstream.
	_, bare := BeginOp(ctx, OpKindCall, "Query.version", OpOpts{})
	bare.SetIdent("xxh3:no-inputs")
	bare.End(OutcomeExecuted)

	var buf bytes.Buffer
	if err := r.WriteDump(&buf, false); err != nil {
		t.Fatal(err)
	}
	header, events, err := ReadDump(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var found, bareFound bool
	for _, ev := range events {
		switch header.Strings[ev.IdentID] {
		case "xxh3:call-digest":
			found = true
			if ev.InputsID == 0 {
				t.Fatal("call with inputs must carry a non-zero InputsID")
			}
			if got := header.Strings[ev.InputsID]; got != `["xxh3:input-a","xxh3:input-b"]` {
				t.Fatalf("InputsID string = %q, want the canonical JSON array", got)
			}
		case "xxh3:no-inputs":
			bareFound = true
			if ev.InputsID != 0 {
				t.Fatalf("input-less call must intern no InputsID, got %d", ev.InputsID)
			}
		}
	}
	if !found || !bareFound {
		t.Fatalf("missing op events: found=%v bareFound=%v", found, bareFound)
	}
}
