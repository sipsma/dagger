package schema

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/core/resourceid"
	"github.com/dagger/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

type InterfaceType struct {
	api       *APIServer
	sourceMod *UserMod
	typeDef   *core.InterfaceTypeDef
}

var _ ModType = (*InterfaceType)(nil)

func newModIface(ctx context.Context, mod *UserMod, typeDef *core.TypeDef) (*InterfaceType, error) {
	if typeDef.Kind != core.TypeDefKindInterface {
		return nil, fmt.Errorf("expected interface type def, got %s", typeDef.Kind)
	}
	iface := &InterfaceType{
		api:       mod.api,
		sourceMod: mod,
		typeDef:   typeDef.AsInterface,
	}
	return iface, nil
}

func (iface *InterfaceType) ConvertFromSDKResult(ctx context.Context, value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	switch value := value.(type) {
	case string:
		// TODO: this needs to handle core IDs too; need a common func for decoding both those and mod objects?

		objMap, modDgst, typeName, err := resourceid.DecodeModuleID(value, "")
		if err != nil {
			return nil, fmt.Errorf("failed to decode id: %w", err)
		}
		sourceMod, err := iface.api.GetModFromDagDigest(ctx, modDgst)
		if err != nil {
			return nil, fmt.Errorf("failed to get source mod %s: %w", modDgst, err)
		}

		modType, ok, err := sourceMod.ModTypeFor(ctx, &core.TypeDef{
			Kind: core.TypeDefKindObject,
			AsObject: &core.ObjectTypeDef{
				Name: typeName,
			},
		}, false)
		if err != nil {
			return nil, fmt.Errorf("failed to get mod type for %s: %w", typeName, err)
		}
		if !ok {
			return nil, fmt.Errorf("failed to find mod type for %s", typeName)
		}

		convertedValue, err := modType.ConvertFromSDKResult(ctx, objMap)
		if err != nil {
			return nil, fmt.Errorf("failed to convert from sdk result: %w", err)
		}

		return &interfaceRuntimeValue{
			Value:          convertedValue,
			UnderlyingType: modType,
			IfaceType:      iface,
		}, nil

	case map[string]any:
		// TODO: can have a helpful error message or just update to handle return of objects from own module as interface
		return nil, fmt.Errorf("unexpected interface value type for conversion from sdk result %T", value)
	default:
		return nil, fmt.Errorf("unexpected interface value type for conversion from sdk result %T", value)
	}
}

func (iface *InterfaceType) ConvertToSDKInput(ctx context.Context, value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	switch value := value.(type) {
	case string:
		return value, nil

	case *interfaceRuntimeValue:
		// TODO: kludge to deal with inconsistency in how user mod objects are currently provided SDKs.
		// For interfaces specifically, we actually do need to provide the object ID rather than the json
		// serialization of the object directly. Otherwise we'll lose track of what the underlying object
		// is.
		userModObj, isUserModObj := value.UnderlyingType.(*UserModObject)
		if isUserModObj {
			convertedValue, err := userModObj.ConvertToID(ctx, value.Value)
			if err != nil {
				return nil, fmt.Errorf("failed to convert to id: %w", err)
			}
			return convertedValue, nil
		}

		convertedValue, err := value.UnderlyingType.ConvertToSDKInput(ctx, value.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to convert to sdk input: %w", err)
		}
		return convertedValue, nil

	default:
		return nil, fmt.Errorf("unexpected interface value type for conversion to sdk input %T", value)
	}
}

func (iface *InterfaceType) SourceMod() Mod {
	return iface.sourceMod
}

func (iface *InterfaceType) GraphqlRuntimeType(ctx context.Context) (graphql.Type, error) {
	schema, err := iface.sourceMod.DependencySchema(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get dependency schema: %w", err)
	}

	gqlType := schema.Compiled.Type(iface.typeDef.Name)
	if gqlType == nil {
		return nil, fmt.Errorf("failed to get type %q from schema", iface.typeDef.Name)
	}
	return gqlType, nil
}

func (iface *InterfaceType) Schema(ctx context.Context) (*ast.SchemaDocument, Resolvers, error) {
	ifaceTypeDef := iface.typeDef
	ifaceName := gqlObjectName(ifaceTypeDef.Name)

	typeSchemaDoc := &ast.SchemaDocument{}
	queryResolver := ObjectResolver{}
	typeSchemaResolvers := Resolvers{
		"Query": queryResolver,
	}

	astIDDef := &ast.Definition{
		Name:        ifaceName + "ID",
		Description: formatGqlDescription("%s identifier", ifaceName),
		Kind:        ast.Scalar,
	}
	typeSchemaDoc.Definitions = append(typeSchemaDoc.Definitions, astIDDef)
	idResolver := stringResolver[string]()
	typeSchemaResolvers[astIDDef.Name] = idResolver

	astLoadDef := &ast.FieldDefinition{
		Name:        fmt.Sprintf("load%sFromID", ifaceName),
		Description: formatGqlDescription("Loads a %s from an ID", ifaceName),
		Arguments: ast.ArgumentDefinitionList{
			&ast.ArgumentDefinition{
				Name: "id",
				Type: ast.NonNullNamedType(astIDDef.Name, nil),
			},
		},
		Type: ast.NonNullNamedType(ifaceName, nil),
	}
	typeSchemaDoc.Extensions = append(typeSchemaDoc.Extensions, &ast.Definition{
		Name:   "Query",
		Kind:   ast.Object,
		Fields: ast.FieldList{astLoadDef},
	})
	queryResolver[astLoadDef.Name] = func(p graphql.ResolveParams) (any, error) {
		return iface.ConvertFromSDKResult(p.Context, p.Args["id"])
	}

	astIfaceDef := &ast.Definition{
		Name:        ifaceName,
		Description: formatGqlDescription(ifaceTypeDef.Description),
		Kind:        ast.Interface,
		Fields: []*ast.FieldDefinition{{
			Name:        "id",
			Description: formatGqlDescription("A unique identifier for this %s", ifaceName),
			Type:        ast.NonNullNamedType(astIDDef.Name, nil),
		}},
	}
	typeSchemaDoc.Definitions = append(typeSchemaDoc.Definitions, astIfaceDef)
	for _, fnTypeDef := range iface.typeDef.Functions {
		fnName := gqlFieldName(fnTypeDef.Name)

		returnASTType, err := typeDefToASTType(fnTypeDef.ReturnType, false)
		if err != nil {
			return nil, nil, err
		}
		// TODO: validate return type use of non-core concrete objects?

		fieldDef := &ast.FieldDefinition{
			Name:        fnName,
			Description: formatGqlDescription(fnTypeDef.Description),
			Type:        returnASTType,
		}

		for _, argMetadata := range fnTypeDef.Args {
			argASTType, err := typeDefToASTType(argMetadata.TypeDef, true)
			if err != nil {
				return nil, nil, err
			}

			// TODO: default values? Or should that not apply to interfaces?

			// TODO: validate arg type use of non-core concrete objects?

			argDef := &ast.ArgumentDefinition{
				Name:        gqlArgName(argMetadata.Name),
				Description: formatGqlDescription(argMetadata.Description),
				Type:        argASTType,
			}
			fieldDef.Arguments = append(fieldDef.Arguments, argDef)
		}

		astIfaceDef.Fields = append(astIfaceDef.Fields, fieldDef)
	}
	typeSchemaResolvers[astIfaceDef.Name] = &InterfaceResolver{
		ResolveType: func(p graphql.ResolveTypeParams) *graphql.Object {
			objVal, ok := p.Value.(*interfaceRuntimeValue)
			if !ok {
				panic(fmt.Errorf("unexpected value type %T", p.Value))
			}
			runtimeType, err := objVal.UnderlyingType.GraphqlRuntimeType(p.Context)
			if err != nil {
				panic(fmt.Errorf("failed to get runtime type: %w", err))
			}
			runtimeObjectType, ok := runtimeType.(*graphql.Object)
			if !ok {
				panic(fmt.Errorf("expected object type, got %T", runtimeType))
			}
			runtimeObjectType = cloneGraphqlRuntimeObject(runtimeObjectType)

			astIfaceRuntimeType := p.Info.Schema.Type(astIfaceDef.Name)
			if astIfaceRuntimeType == nil {
				panic(fmt.Errorf("failed to get ast type for interface %q", astIfaceDef.Name))
			}
			astIfaceRuntimeIface, ok := astIfaceRuntimeType.(*graphql.Interface)
			if !ok {
				panic(fmt.Errorf("expected interface type for %s, got %T", astIfaceDef.Name, astIfaceRuntimeType))
			}
			astIfaceIDField, ok := astIfaceRuntimeIface.Fields()["id"]
			if !ok {
				panic(fmt.Errorf("failed to get id field for interface %q", astIfaceDef.Name))
			}

			runtimeObjectType.AddFieldConfig("id", &graphql.Field{
				Type: astIfaceIDField.Type,
				Resolve: func(p graphql.ResolveParams) (any, error) {
					// TODO: kludge to deal with inconsistency in how user mod objects are currently provided SDKs.
					// For interfaces specifically, we actually do need to provide the object ID rather than the json
					// serialization of the object directly. Otherwise we'll lose track of what the underlying object
					// is.
					userModObj, isUserModObj := objVal.UnderlyingType.(*UserModObject)
					if isUserModObj {
						convertedValue, err := userModObj.ConvertToID(p.Context, objVal.Value)
						if err != nil {
							return nil, fmt.Errorf("failed to convert to id: %w", err)
						}
						return convertedValue, nil
					}

					convertedValue, err := objVal.UnderlyingType.ConvertToSDKInput(p.Context, objVal.Value)
					if err != nil {
						return nil, fmt.Errorf("failed to convert to id: %w", err)
					}
					return convertedValue, nil
				},
			})

			return runtimeObjectType
		},
	}

	return typeSchemaDoc, typeSchemaResolvers, nil
}

type interfaceRuntimeValue struct {
	Value          any
	UnderlyingType ModType
	IfaceType      *InterfaceType
}

// allow default graphql resolver to use this object transparently:
// https://github.com/dagger/graphql/blob/bc781b6f799136194783ccb09d27d07590704b32/executor.go#L930-L945
func (v *interfaceRuntimeValue) Resolve(p graphql.ResolveParams) (any, error) {
	p.Source = v.Value
	return graphql.DefaultResolveFn(p)
}

func (v *interfaceRuntimeValue) MarshalJSON() ([]byte, error) {
	return json.Marshal(v.Value)
}

func (v *interfaceRuntimeValue) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &v.Value)
}

func cloneGraphqlRuntimeObject(obj *graphql.Object) *graphql.Object {
	newObj := graphql.NewObject(graphql.ObjectConfig{
		Name:        obj.PrivateName,
		Description: obj.PrivateDescription,
		IsTypeOf:    obj.IsTypeOf,
		Fields:      graphql.Fields{},
	})

	for fieldName, fieldDef := range obj.Fields() {
		var args graphql.FieldConfigArgument
		for _, argDef := range fieldDef.Args {
			args = append(args, &graphql.ArgumentConfig{
				Name:         argDef.PrivateName,
				Description:  argDef.PrivateDescription,
				Type:         argDef.Type,
				DefaultValue: argDef.DefaultValue,
			})
		}

		newObj.AddFieldConfig(fieldName, &graphql.Field{
			Name:              fieldDef.Name,
			Description:       fieldDef.Description,
			Type:              fieldDef.Type,
			Resolve:           fieldDef.Resolve,
			Subscribe:         fieldDef.Subscribe,
			DeprecationReason: fieldDef.DeprecationReason,
			Args:              args,
		})
	}

	return newObj
}
