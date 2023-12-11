package schema

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/core"
	"github.com/dagger/graphql"
	"github.com/opencontainers/go-digest"
)

// CoreMod is a special implementation of Mod for our core API, which is not *technically* a true module yet
// but can be treated as one in terms of dependencies. It has no dependencies itself and is currently an
// implicit dependency of every user module.
type CoreMod struct {
	api            *APIServer
	compiledSchema *CompiledSchema
}

var _ Mod = (*CoreMod)(nil)

func (m *CoreMod) Name() string {
	return coreModuleName
}

func (m *CoreMod) DagDigest() digest.Digest {
	// core is always a leaf, so we just return a static digest
	return digest.FromString(coreModuleName)
}

func (m *CoreMod) Dependencies() []Mod {
	return nil
}

func (m *CoreMod) Schema(_ context.Context) ([]SchemaResolvers, error) {
	return []SchemaResolvers{m.compiledSchema}, nil
}

func (m *CoreMod) ModTypeFor(ctx context.Context, typeDef *core.TypeDef, checkDirectDeps bool) (ModType, bool, error) {
	switch typeDef.Kind {
	case core.TypeDefKindString, core.TypeDefKindInteger, core.TypeDefKindBoolean, core.TypeDefKindVoid:
		return &PrimitiveType{api: m.api, kind: typeDef.Kind}, true, nil

	case core.TypeDefKindList:
		underlyingType, ok, err := m.ModTypeFor(ctx, typeDef.AsList.ElementTypeDef, checkDirectDeps)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get underlying type: %w", err)
		}
		if !ok {
			return nil, false, nil
		}
		return &ListType{underlying: underlyingType}, true, nil

	case core.TypeDefKindObject:
		typeName := gqlObjectName(typeDef.AsObject.Name)
		resolver, ok := m.compiledSchema.Resolvers()[typeName]
		if !ok {
			return nil, false, nil
		}
		idableResolver, ok := resolver.(IDableObjectResolver)
		if !ok {
			return nil, false, nil
		}
		return &CoreModObject{
			coreMod:  m,
			resolver: idableResolver,
			name:     typeName,
		}, true, nil

	case core.TypeDefKindInterface:
		// core does not yet defined any interfaces
		return nil, false, nil

	default:
		return nil, false, fmt.Errorf("unexpected type def kind %s", typeDef.Kind)
	}
}

// CoreModObject represents objects from core (Container, Directory, etc.)
type CoreModObject struct {
	coreMod  *CoreMod
	resolver IDableObjectResolver
	// TODO: kludgy?
	name string
}

var _ ModType = (*CoreModObject)(nil)

func (obj *CoreModObject) ConvertFromSDKResult(_ context.Context, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	id, ok := value.(string)
	if !ok {
		return value, nil
	}
	return obj.resolver.FromID(id)
}

func (obj *CoreModObject) ConvertToSDKInput(ctx context.Context, value any) (any, error) {
	return obj.resolver.ToID(value)
}

func (obj *CoreModObject) SourceMod() Mod {
	return obj.coreMod
}

func (obj *CoreModObject) GraphqlRuntimeType(ctx context.Context) (graphql.Type, error) {
	gqlType := obj.coreMod.compiledSchema.Compiled.Type(obj.name)
	if gqlType == nil {
		return nil, fmt.Errorf("no type named %s", obj.name)
	}
	return gqlType, nil
}
