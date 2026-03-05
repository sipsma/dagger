package dagql

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dagger/dagger/dagql/call"
	"github.com/vektah/gqlparser/v2/ast"
)

const (
	persistedResultKindNull   = "null"
	persistedResultKindObject = "object_id"
	persistedResultKindScalar = "scalar_json"
	persistedResultKindList   = "list"
)

// PersistedResultEnvelope is the shared on-disk payload envelope for persisted
// result self values.
//
// This is intentionally opaque at the DB level (stored as self_payload bytes),
// while still carrying enough structured data to decode common SDK-return
// shapes (scalars, object IDs, lists, nested combinations).
type PersistedResultEnvelope struct {
	Version      int                       `json:"version"`
	Kind         string                    `json:"kind"`
	TypeName     string                    `json:"typeName,omitempty"`
	ID           string                    `json:"id,omitempty"`
	ScalarJSON   json.RawMessage           `json:"scalarJSON,omitempty"`
	ElemTypeName string                    `json:"elemTypeName,omitempty"`
	Items        []PersistedResultEnvelope `json:"items,omitempty"`
}

// PersistedSelfCodec is the shared interface used to encode/decode result self
// payloads for disk persistence.
type PersistedSelfCodec interface {
	EncodeResult(context.Context, AnyResult) (PersistedResultEnvelope, error)
	DecodeResult(context.Context, PersistedResultEnvelope) (AnyResult, error)
}

type defaultPersistedSelfCodec struct{}

var DefaultPersistedSelfCodec PersistedSelfCodec = defaultPersistedSelfCodec{}

func (defaultPersistedSelfCodec) EncodeResult(ctx context.Context, res AnyResult) (PersistedResultEnvelope, error) {
	_ = ctx
	return encodePersistedResultEnvelope(res)
}

func (defaultPersistedSelfCodec) DecodeResult(ctx context.Context, env PersistedResultEnvelope) (AnyResult, error) {
	return decodePersistedResultEnvelope(ctx, env)
}

func encodePersistedResultEnvelope(res AnyResult) (PersistedResultEnvelope, error) {
	if res == nil {
		return PersistedResultEnvelope{
			Version: 1,
			Kind:    persistedResultKindNull,
		}, nil
	}
	if res.ID() == nil {
		return PersistedResultEnvelope{}, fmt.Errorf("encode persisted result envelope: nil result ID")
	}
	encID, err := res.ID().Encode()
	if err != nil {
		return PersistedResultEnvelope{}, fmt.Errorf("encode persisted result envelope ID: %w", err)
	}

	if _, ok := res.(AnyObjectResult); ok {
		return PersistedResultEnvelope{
			Version:  1,
			Kind:     persistedResultKindObject,
			TypeName: res.Type().Name(),
			ID:       encID,
		}, nil
	}

	if enumerable, ok := res.Unwrap().(Enumerable); ok {
		itemEnvs := make([]PersistedResultEnvelope, 0, enumerable.Len())
		for i := 1; i <= enumerable.Len(); i++ {
			item, err := res.NthValue(i)
			if err != nil {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted list item %d: %w", i, err)
			}
			itemEnv, err := encodePersistedResultEnvelope(item)
			if err != nil {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted list item %d envelope: %w", i, err)
			}
			itemEnvs = append(itemEnvs, itemEnv)
		}
		return PersistedResultEnvelope{
			Version:      1,
			Kind:         persistedResultKindList,
			TypeName:     res.Type().Name(),
			ID:           encID,
			ElemTypeName: enumerable.Element().Type().Name(),
			Items:        itemEnvs,
		}, nil
	}

	scalarJSON, err := json.Marshal(res.Unwrap())
	if err != nil {
		return PersistedResultEnvelope{}, fmt.Errorf("encode scalar_json payload: %w", err)
	}
	return PersistedResultEnvelope{
		Version:    1,
		Kind:       persistedResultKindScalar,
		TypeName:   res.Type().Name(),
		ID:         encID,
		ScalarJSON: scalarJSON,
	}, nil
}

func decodePersistedResultEnvelope(ctx context.Context, env PersistedResultEnvelope) (AnyResult, error) {
	switch env.Kind {
	case persistedResultKindNull:
		return nil, nil
	case persistedResultKindObject:
		id, err := decodeEnvelopeID(env)
		if err != nil {
			return nil, err
		}
		srv := CurrentDagqlServer(ctx)
		if srv == nil {
			return nil, fmt.Errorf("decode object_id envelope: missing current dagql server in context")
		}
		val, err := srv.Load(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("decode object_id envelope load: %w", err)
		}
		return val, nil
	case persistedResultKindScalar:
		id, err := decodeEnvelopeID(env)
		if err != nil {
			return nil, err
		}
		srv := CurrentDagqlServer(ctx)
		if srv == nil {
			return nil, fmt.Errorf("decode scalar_json envelope: missing current dagql server in context")
		}
		scalarType, ok := srv.ScalarType(env.TypeName)
		if !ok {
			return nil, fmt.Errorf("decode scalar_json envelope: unknown scalar type %q", env.TypeName)
		}
		var raw any
		if err := json.Unmarshal(env.ScalarJSON, &raw); err != nil {
			return nil, fmt.Errorf("decode scalar_json envelope payload: %w", err)
		}
		input, err := scalarType.DecodeInput(raw)
		if err != nil {
			return nil, fmt.Errorf("decode scalar_json envelope input: %w", err)
		}
		return NewResultForID(input, id)
	case persistedResultKindList:
		id, err := decodeEnvelopeID(env)
		if err != nil {
			return nil, err
		}
		items := make([]AnyResult, 0, len(env.Items))
		for i, itemEnv := range env.Items {
			itemCtx := ctx
			if itemEnv.ID != "" {
				itemID, decodeIDErr := decodeEnvelopeID(itemEnv)
				if decodeIDErr != nil {
					return nil, fmt.Errorf("decode list item %d ID: %w", i+1, decodeIDErr)
				}
				itemCtx = ContextWithID(itemCtx, itemID)
			}
			itemRes, err := decodePersistedResultEnvelope(itemCtx, itemEnv)
			if err != nil {
				return nil, fmt.Errorf("decode list item %d: %w", i+1, err)
			}
			items = append(items, itemRes)
		}

		var elem Typed
		for _, item := range items {
			if item == nil {
				continue
			}
			elem = item.Unwrap()
			break
		}
		if elem == nil {
			elem = persistedTypedRef{name: env.ElemTypeName}
		}

		return NewResultForID(DynamicResultArrayOutput{
			Elem:   elem,
			Values: items,
		}, id)
	default:
		return nil, fmt.Errorf("decode persisted result envelope: unsupported kind %q", env.Kind)
	}
}

func decodeEnvelopeID(env PersistedResultEnvelope) (*call.ID, error) {
	if env.ID == "" {
		return nil, fmt.Errorf("decode persisted result envelope: empty ID for kind %q", env.Kind)
	}
	var id call.ID
	if err := id.Decode(env.ID); err != nil {
		return nil, fmt.Errorf("decode persisted result envelope ID: %w", err)
	}
	return &id, nil
}

// PersistedSnapshotRefLink is a generic non-opaque link from a persisted result
// self payload to one durable snapshot ref key.
type PersistedSnapshotRefLink struct {
	RefKey string
	Role   string
	Slot   string
}

// PersistedSnapshotRefLinkProvider is the shared interface used by persistable
// self payloads to expose snapshot ref links for `result_snapshot_refs`.
type PersistedSnapshotRefLinkProvider interface {
	PersistedSnapshotRefLinks() []PersistedSnapshotRefLink
}

func persistedSnapshotLinksFromTyped(self Typed) []PersistedSnapshotRefLink {
	if self == nil {
		return nil
	}
	linker, ok := any(self).(PersistedSnapshotRefLinkProvider)
	if !ok {
		return nil
	}
	links := linker.PersistedSnapshotRefLinks()
	if len(links) == 0 {
		return nil
	}
	cpy := make([]PersistedSnapshotRefLink, len(links))
	copy(cpy, links)
	return cpy
}

type persistedTypedRef struct {
	name string
}

func (r persistedTypedRef) Type() *ast.Type {
	return &ast.Type{
		NamedType: r.name,
		NonNull:   true,
	}
}
