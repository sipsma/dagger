package templates

import (
	"strings"

	"github.com/dagger/dagger/cmd/codegen/introspection"
)

// FormatTypeFunc is an implementation of generator.FormatTypeFuncs interface
// to format GraphQL type into Golang.
type FormatTypeFunc struct{}

func (f *FormatTypeFunc) FormatKindList(representation string) string {
	representation = "[]" + representation
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarString(representation string) string {
	representation += "string"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarInt(representation string) string {
	representation += "int"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarFloat(representation string) string {
	representation += "float"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarBoolean(representation string) string {
	representation += "bool"
	return representation
}

func (f *FormatTypeFunc) FormatKindScalarDefault(schema *introspection.Schema, representation string, refName string, input bool) string {
	if obj, rest, ok := strings.Cut(refName, "ID"); input && ok && rest == "" {
		objType := schema.Types.Get(obj)
		switch objType.Kind {
		case introspection.TypeKindObject:
			representation += "*" + obj
		case introspection.TypeKindInterface:
			representation += obj
		}
	} else {
		representation += refName
	}

	return representation
}

func (f *FormatTypeFunc) FormatKindObject(representation string, refName string, input bool) string {
	representation += formatName(refName)
	return representation
}

func (f *FormatTypeFunc) FormatKindInterface(representation string, refName string, input bool) string {
	representation += formatName(refName)
	return representation
}

func (f *FormatTypeFunc) FormatKindInputObject(representation string, refName string, input bool) string {
	representation += formatName(refName)
	return representation
}

func (f *FormatTypeFunc) FormatKindEnum(representation string, refName string) string {
	representation += refName
	return representation
}
