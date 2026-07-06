package dagql

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Persisted payloads (object JSON and lazy fragments) reference other
// persisted results and encoded call IDs through structured ref tokens
// rather than bare values. The token's JSON form is a single-key object
// holding a reserved key, which gives the persistence layer a generic,
// non-heuristic way to locate every cross-result reference in otherwise
// opaque payload bytes: any JSON object holding a reserved key is a ref;
// anything else is data. Bundle import rewrites refs from the exporting
// store's ID space into the local one by walking payloads for these tokens.
const (
	persistedResultRefJSONKey = "$dagqlResultRef"
	persistedCallIDJSONKey    = "$dagqlCallID"
)

// PersistedResultRef is a reference to another persisted result inside a
// persisted payload, carrying that result's ID in the store's ID space.
// JSON form: {"$dagqlResultRef": N}. The zero ref means "no reference" and
// is what omitempty elides.
type PersistedResultRef uint64

func (r PersistedResultRef) ResultID() uint64 {
	return uint64(r)
}

func NewPersistedResultRef(resultID uint64) PersistedResultRef {
	return PersistedResultRef(resultID)
}

func (r PersistedResultRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]uint64{persistedResultRefJSONKey: uint64(r)})
}

func (r *PersistedResultRef) UnmarshalJSON(data []byte) error {
	resultID, err := decodeSingleKeyToken[uint64](data, persistedResultRefJSONKey)
	if err != nil {
		return err
	}
	*r = PersistedResultRef(resultID)
	return nil
}

// PersistedCallID is an encoded dagql call ID inside a persisted payload.
// JSON form: {"$dagqlCallID": "<encoded>"}. Handle-form IDs embed an
// engine-local result ID, which is why call IDs must be locatable for
// rewrite at bundle import; recipe-form IDs reference only intra-recipe
// call digests and cross unchanged. The empty value means "no ID" and is
// what omitempty elides.
type PersistedCallID string

func (id PersistedCallID) Encoded() string {
	return string(id)
}

func NewPersistedCallID(encoded string) PersistedCallID {
	return PersistedCallID(encoded)
}

func (id PersistedCallID) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{persistedCallIDJSONKey: string(id)})
}

func (id *PersistedCallID) UnmarshalJSON(data []byte) error {
	encoded, err := decodeSingleKeyToken[string](data, persistedCallIDJSONKey)
	if err != nil {
		return err
	}
	*id = PersistedCallID(encoded)
	return nil
}

// decodeSingleKeyToken parses a ref token: exactly one object with exactly
// the reserved key and a value of the expected primitive type. Anything
// else is a malformed token and fails loudly — tokens are a contract, and
// contract violations are fixed at the encode site, never tolerated here.
func decodeSingleKeyToken[T any](data []byte, key string) (T, error) {
	var zero T
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return zero, fmt.Errorf("decode %s token: %w", key, err)
	}
	raw, ok := obj[key]
	if !ok {
		return zero, fmt.Errorf("decode %s token: missing reserved key", key)
	}
	if len(obj) != 1 {
		return zero, fmt.Errorf("decode %s token: unexpected extra keys", key)
	}
	var val T
	if err := json.Unmarshal(raw, &val); err != nil {
		return zero, fmt.Errorf("decode %s token value: %w", key, err)
	}
	return val, nil
}

// rewritePersistedPayloadRefs walks raw persisted payload JSON and rewrites
// every ref token through the given callbacks, returning the rewritten
// payload. Non-token leaf values pass through byte-verbatim (containers are
// re-encoded around them). A JSON object holding a reserved key must be
// exactly a well-formed token — a reserved key in any other shape is a
// loud error, never skipped data (the walk is a contract, not a heuristic).
func rewritePersistedPayloadRefs(
	raw json.RawMessage,
	rewriteResultRef func(uint64) (uint64, error),
	rewriteCallID func(string) (string, error),
) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, nil
	}
	return rewriteRefsInValue(raw, rewriteResultRef, rewriteCallID)
}

// rewriteResultCallRefs rewrites every ResultCallRef.ResultID reachable
// from the frame — receiver, module, args and implicit-input literals,
// and inline sub-frames — through remap. The frame must be an unshared
// tree (decoded from persisted JSON or cloned); rewrite is in place. A
// remap failure means the referenced row is not available in the target
// ID space, which the caller treats as a missing dependency of the
// referring row.
func rewriteResultCallRefs(frame *ResultCall, remap func(uint64) (uint64, error)) error {
	var walkCall func(*ResultCall) error
	var walkRef func(*ResultCallRef) error
	var walkLiteral func(*ResultCallLiteral) error
	var walkArgs func([]*ResultCallArg) error

	walkRef = func(ref *ResultCallRef) error {
		if ref == nil {
			return nil
		}
		if ref.ResultID != 0 {
			rewritten, err := remap(ref.ResultID)
			if err != nil {
				return err
			}
			ref.ResultID = rewritten
		}
		return walkCall(ref.Call)
	}
	walkLiteral = func(lit *ResultCallLiteral) error {
		if lit == nil {
			return nil
		}
		switch lit.Kind {
		case ResultCallLiteralKindResultRef:
			return walkRef(lit.ResultRef)
		case ResultCallLiteralKindList:
			for i, item := range lit.ListItems {
				if err := walkLiteral(item); err != nil {
					return fmt.Errorf("list item %d: %w", i, err)
				}
			}
		case ResultCallLiteralKindObject:
			for _, field := range lit.ObjectFields {
				if field == nil {
					continue
				}
				if err := walkLiteral(field.Value); err != nil {
					return fmt.Errorf("field %q: %w", field.Name, err)
				}
			}
		}
		return nil
	}
	walkArgs = func(args []*ResultCallArg) error {
		for _, arg := range args {
			if arg == nil {
				continue
			}
			if err := walkLiteral(arg.Value); err != nil {
				return fmt.Errorf("arg %q: %w", arg.Name, err)
			}
		}
		return nil
	}
	walkCall = func(call *ResultCall) error {
		if call == nil {
			return nil
		}
		if err := walkRef(call.Receiver); err != nil {
			return fmt.Errorf("receiver: %w", err)
		}
		if call.Module != nil {
			if err := walkRef(call.Module.ResultRef); err != nil {
				return fmt.Errorf("module: %w", err)
			}
		}
		if err := walkArgs(call.Args); err != nil {
			return err
		}
		if err := walkArgs(call.ImplicitInputs); err != nil {
			return err
		}
		return nil
	}
	return walkCall(frame)
}

func rewriteRefsInValue(
	raw json.RawMessage,
	rewriteResultRef func(uint64) (uint64, error),
	rewriteCallID func(string) (string, error),
) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	switch trimmed[0] {
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil, fmt.Errorf("rewrite payload refs: parse object: %w", err)
		}

		if _, isRef := obj[persistedResultRefJSONKey]; isRef {
			var token PersistedResultRef
			if err := token.UnmarshalJSON(trimmed); err != nil {
				return nil, err
			}
			rewritten, err := rewriteResultRef(uint64(token))
			if err != nil {
				return nil, err
			}
			return PersistedResultRef(rewritten).MarshalJSON()
		}
		if _, isCallID := obj[persistedCallIDJSONKey]; isCallID {
			var token PersistedCallID
			if err := token.UnmarshalJSON(trimmed); err != nil {
				return nil, err
			}
			rewritten, err := rewriteCallID(string(token))
			if err != nil {
				return nil, err
			}
			return PersistedCallID(rewritten).MarshalJSON()
		}

		changed := false
		for key, val := range obj {
			newVal, err := rewriteRefsInValue(val, rewriteResultRef, rewriteCallID)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", key, err)
			}
			if !bytes.Equal(newVal, val) {
				obj[key] = newVal
				changed = true
			}
		}
		if !changed {
			return raw, nil
		}
		return json.Marshal(obj)
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("rewrite payload refs: parse array: %w", err)
		}
		changed := false
		for i, val := range arr {
			newVal, err := rewriteRefsInValue(val, rewriteResultRef, rewriteCallID)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			if !bytes.Equal(newVal, val) {
				arr[i] = newVal
				changed = true
			}
		}
		if !changed {
			return raw, nil
		}
		return json.Marshal(arr)
	default:
		// Scalar leaf: data, byte-verbatim.
		return raw, nil
	}
}
