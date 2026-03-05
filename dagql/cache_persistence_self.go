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
	persistedResultKindObject = "object_self"
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
	ObjectJSON   json.RawMessage           `json:"objectJSON,omitempty"`
	ScalarJSON   json.RawMessage           `json:"scalarJSON,omitempty"`
	ElemTypeName string                    `json:"elemTypeName,omitempty"`
	Items        []PersistedResultEnvelope `json:"items,omitempty"`
}

// PersistedObject is implemented by object self payloads that can be encoded
// directly for import-time cache persistence.
type PersistedObject interface {
	Typed
	EncodePersistedObject(context.Context) (json.RawMessage, error)
}

// PersistedObjectDecoder is implemented by zero-value object types that know
// how to reconstruct a persisted object self payload without replaying the
// original dagql call chain.
type PersistedObjectDecoder interface {
	Typed
	DecodePersistedObject(context.Context, *call.ID, json.RawMessage, PersistedObjectResolver) (Typed, error)
}

// PersistedObjectResolver resolves already-imported persisted results by ID for
// object self decode.
type PersistedObjectResolver interface {
	LoadPersistedResult(context.Context, *call.ID) (AnyResult, error)
	LoadPersistedObject(context.Context, *call.ID) (AnyObjectResult, error)
	PersistedSnapshotLinks(context.Context, *call.ID) ([]PersistedSnapshotRefLink, error)
}

type persistedObjectResolverKey struct{}

func ContextWithPersistedObjectResolver(ctx context.Context, resolver PersistedObjectResolver) context.Context {
	return context.WithValue(ctx, persistedObjectResolverKey{}, resolver)
}

func currentPersistedObjectResolver(ctx context.Context) PersistedObjectResolver {
	resolver, _ := ctx.Value(persistedObjectResolverKey{}).(PersistedObjectResolver)
	return resolver
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
	return encodePersistedResultEnvelope(ctx, res)
}

func (defaultPersistedSelfCodec) DecodeResult(ctx context.Context, env PersistedResultEnvelope) (AnyResult, error) {
	return decodePersistedResultEnvelope(ctx, env)
}

func encodePersistedResultEnvelope(ctx context.Context, res AnyResult) (PersistedResultEnvelope, error) {
	if res == nil {
		return PersistedResultEnvelope{
			Version: 2,
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

	if srv := CurrentDagqlServer(ctx); srv != nil && res.Type() != nil {
		if _, ok := srv.ObjectType(res.Type().Name()); ok {
			encoder, ok := res.Unwrap().(PersistedObject)
			if !ok {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted object payload: type %q does not implement persisted object encoding", res.Type().Name())
			}
			objectJSON, err := encoder.EncodePersistedObject(ctx)
			if err != nil {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted object payload: %w", err)
			}
			return PersistedResultEnvelope{
				Version:    2,
				Kind:       persistedResultKindObject,
				TypeName:   res.Type().Name(),
				ID:         encID,
				ObjectJSON: objectJSON,
			}, nil
		}
	}
	if encoder, ok := res.Unwrap().(PersistedObject); ok {
		objectJSON, err := encoder.EncodePersistedObject(ctx)
		if err != nil {
			return PersistedResultEnvelope{}, fmt.Errorf("encode persisted object payload: %w", err)
		}
		return PersistedResultEnvelope{
			Version:    2,
			Kind:       persistedResultKindObject,
			TypeName:   res.Type().Name(),
			ID:         encID,
			ObjectJSON: objectJSON,
		}, nil
	}

	if enumerable, ok := res.Unwrap().(Enumerable); ok {
		itemEnvs := make([]PersistedResultEnvelope, 0, enumerable.Len())
		for i := 1; i <= enumerable.Len(); i++ {
			item, err := res.NthValue(i)
			if err != nil {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted list item %d: %w", i, err)
			}
			itemEnv, err := encodePersistedResultEnvelope(ctx, item)
			if err != nil {
				return PersistedResultEnvelope{}, fmt.Errorf("encode persisted list item %d envelope: %w", i, err)
			}
			itemEnvs = append(itemEnvs, itemEnv)
		}
		return PersistedResultEnvelope{
			Version:      2,
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
		Version:    2,
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
		objType, ok := srv.ObjectType(env.TypeName)
		if !ok {
			return nil, fmt.Errorf("decode object_id envelope: unknown object type %q", env.TypeName)
		}
		decoder, ok := objType.Typed().(PersistedObjectDecoder)
		if !ok {
			return nil, fmt.Errorf("decode object_id envelope: object type %q does not implement persisted decode", env.TypeName)
		}
		valSelf, err := decoder.DecodePersistedObject(ctx, id, env.ObjectJSON, currentPersistedObjectResolver(ctx))
		if err != nil {
			return nil, fmt.Errorf("decode object_id envelope load: %w", err)
		}
		valRes, err := NewResultForID(valSelf, id)
		if err != nil {
			return nil, fmt.Errorf("decode object_id envelope result: %w", err)
		}
		objRes, err := objType.New(valRes)
		if err != nil {
			return nil, fmt.Errorf("decode object_id envelope instantiate: %w", err)
		}
		return objRes, nil
	case persistedResultKindScalar:
		id, err := decodeEnvelopeID(env)
		if err != nil {
			return nil, err
		}
		srv := CurrentDagqlServer(ctx)
		var raw any
		if err := json.Unmarshal(env.ScalarJSON, &raw); err != nil {
			return nil, fmt.Errorf("decode scalar_json envelope payload: %w", err)
		}
		if srv != nil {
			scalarType, ok := srv.ScalarType(env.TypeName)
			if ok {
				input, err := scalarType.DecodeInput(raw)
				if err != nil {
					return nil, fmt.Errorf("decode scalar_json envelope input: %w", err)
				}
				return NewResultForID(input, id)
			}
		}
		builtin, err := decodeBuiltinPersistedScalar(env.TypeName, raw)
		if err != nil {
			return nil, fmt.Errorf("decode scalar_json envelope builtin input: %w", err)
		}
		return NewResultForID(builtin, id)
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

func decodeBuiltinPersistedScalar(typeName string, raw any) (Typed, error) {
	switch typeName {
	case "String":
		input, err := String("").DecodeInput(raw)
		if err != nil {
			return nil, err
		}
		typed, ok := input.(Typed)
		if !ok {
			return nil, fmt.Errorf("builtin scalar String did not decode to Typed: %T", input)
		}
		return typed, nil
	case "Int":
		input, err := Int(0).DecodeInput(raw)
		if err != nil {
			return nil, err
		}
		typed, ok := input.(Typed)
		if !ok {
			return nil, fmt.Errorf("builtin scalar Int did not decode to Typed: %T", input)
		}
		return typed, nil
	case "Float":
		input, err := Float(0).DecodeInput(raw)
		if err != nil {
			return nil, err
		}
		typed, ok := input.(Typed)
		if !ok {
			return nil, fmt.Errorf("builtin scalar Float did not decode to Typed: %T", input)
		}
		return typed, nil
	case "Boolean":
		input, err := Boolean(false).DecodeInput(raw)
		if err != nil {
			return nil, err
		}
		typed, ok := input.(Typed)
		if !ok {
			return nil, fmt.Errorf("builtin scalar Boolean did not decode to Typed: %T", input)
		}
		return typed, nil
	default:
		return nil, fmt.Errorf("unknown scalar type %q and no dagql server in context", typeName)
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
