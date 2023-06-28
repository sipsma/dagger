package router

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrMergeTypeConflict   = errors.New("object type re-defined")
	ErrMergeFieldConflict  = errors.New("field re-defined")
	ErrMergeScalarConflict = errors.New("scalar re-defined")
)

func MergeLoadedSchemas(name string, schemas ...LoadedSchema) LoadedSchema {
	staticSchemas := make([]StaticSchemaParams, len(schemas))
	for i, s := range schemas {
		staticSchemas[i] = StaticSchemaParams{
			Schema: s.Schema(),
		}
	}
	return StaticSchema(mergeSchemas(name, staticSchemas...))
}

func MergeExecutableSchemas(name string, schemas ...ExecutableSchema) (ExecutableSchema, error) {
	mergedSchema := StaticSchemaParams{
		Name:      name,
		Resolvers: Resolvers{},
	}
	for _, schema := range schemas {
		mergedSchema.Schema += schema.Schema() + "\n"
		for name, resolver := range schema.Resolvers() {
			switch resolver := resolver.(type) {
			case FieldResolvers:
				existing, ok := mergedSchema.Resolvers[name]
				if !ok {
					existing = ObjectResolver{}
				}
				existingObject, ok := existing.(FieldResolvers)
				if !ok {
					return nil, fmt.Errorf("unexpected resolver type %T", existing)
				}
				for fieldName, fieldResolveFn := range resolver.Fields() {
					if _, ok := existingObject.Fields()[fieldName]; ok {
						return nil, fmt.Errorf("conflict on type %q field %q: %w", name, fieldName, ErrMergeFieldConflict)
					}
					existingObject.SetField(fieldName, fieldResolveFn)
				}
				mergedSchema.Resolvers[name] = existingObject
			case ScalarResolver:
				if existing, ok := mergedSchema.Resolvers[name]; ok {
					if _, ok := existing.(ScalarResolver); !ok {
						return nil, fmt.Errorf("conflict on type %q: %w", name, ErrMergeTypeConflict)
					}
					return nil, fmt.Errorf("conflict on type %q: %w", name, ErrMergeScalarConflict)
				}
				mergedSchema.Resolvers[name] = resolver
			}
		}
	}
	return StaticSchema(mergedSchema), nil
}

func mergeSchemas(name string, schemas ...StaticSchemaParams) StaticSchemaParams {
	merged := StaticSchemaParams{Name: name}

	defs := []string{}
	for _, r := range schemas {
		defs = append(defs, r.Schema)
	}
	merged.Schema = strings.Join(defs, "\n")

	return merged
}
