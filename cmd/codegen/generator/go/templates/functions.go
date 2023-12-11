package templates

import (
	"context"
	"fmt"
	"go/token"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/iancoleman/strcase"
	"golang.org/x/tools/go/packages"

	"github.com/dagger/dagger/cmd/codegen/generator"
	"github.com/dagger/dagger/cmd/codegen/introspection"
	"github.com/dagger/dagger/core/modules"
)

func GoTemplateFuncs(
	ctx context.Context,
	schema *introspection.Schema,
	module *modules.Config,
	pkg *packages.Package,
	fset *token.FileSet,
) template.FuncMap {
	return goTemplateFuncs{
		CommonFunctions: generator.NewCommonFunctions(&FormatTypeFunc{}, schema),
		ctx:             ctx,
		module:          module,
		modulePkg:       pkg,
		moduleFset:      fset,
		schema:          schema,
	}.FuncMap()
}

type goTemplateFuncs struct {
	*generator.CommonFunctions
	ctx        context.Context
	module     *modules.Config
	modulePkg  *packages.Package
	moduleFset *token.FileSet
	schema     *introspection.Schema
}

func (funcs goTemplateFuncs) FuncMap() template.FuncMap {
	return template.FuncMap{
		// common
		"FormatReturnType": funcs.FormatReturnType,
		"FormatInputType":  funcs.FormatInputType,
		"FormatOutputType": funcs.FormatOutputType,
		"GetArrayField":    funcs.GetArrayField,
		"IsListOfObject":   funcs.IsListOfObject,
		"ToLowerCase":      funcs.ToLowerCase,
		"ToUpperCase":      funcs.ToUpperCase,
		"ConvertID":        funcs.ConvertID,
		"IsSelfChainable":  funcs.IsSelfChainable,
		"IsIDableObject":   funcs.IsIDableObject,
		"InnerType":        funcs.InnerType,
		"ObjectName":       funcs.ObjectName,

		// go specific
		"Comment":                  funcs.comment,
		"FormatDeprecation":        funcs.formatDeprecation,
		"FormatName":               formatName,
		"FormatIfaceImplName":      formatIfaceImplName,
		"FormatEnum":               funcs.formatEnum,
		"SortEnumFields":           funcs.sortEnumFields,
		"FieldOptionsStructName":   funcs.fieldOptionsStructName,
		"FieldFunction":            funcs.fieldFunction,
		"InterfaceFunction":        funcs.ifaceFunction,
		"IsEnum":                   funcs.isEnum,
		"IsPointer":                funcs.isPointer,
		"FormatArrayField":         funcs.formatArrayField,
		"FormatArrayToSingleType":  funcs.formatArrayToSingleType,
		"ModuleMainSrc":            funcs.moduleMainSrc,
		"FormatConcreteOutputType": funcs.formatConcreteOutputType,
	}
}

// comments out a string
// Example: `hello\nworld` -> `// hello\n// world\n`
func (funcs goTemplateFuncs) comment(s string) string {
	if s == "" {
		return ""
	}

	lines := strings.Split(s, "\n")

	for i, l := range lines {
		lines[i] = "// " + l
	}
	return strings.Join(lines, "\n")
}

// format the deprecation reason
// Example: `Replaced by @foo.` -> `// Replaced by Foo\n`
func (funcs goTemplateFuncs) formatDeprecation(s string) string {
	r := regexp.MustCompile("`[a-zA-Z0-9_]+`")
	matches := r.FindAllString(s, -1)
	for _, match := range matches {
		replacement := strings.TrimPrefix(match, "`")
		replacement = strings.TrimSuffix(replacement, "`")
		replacement = formatName(replacement)
		s = strings.ReplaceAll(s, match, replacement)
	}
	return funcs.comment("Deprecated: " + s)
}

func (funcs goTemplateFuncs) isEnum(t introspection.Type) bool {
	return t.Kind == introspection.TypeKindEnum &&
		// We ignore the internal GraphQL enums
		!strings.HasPrefix(t.Name, "__")
}

// isPointer returns true if value is a pointer.
func (funcs goTemplateFuncs) isPointer(t introspection.InputValue) (bool, error) {
	// Ignore id since it's converted to special ID type later.
	if t.Name == "id" {
		return false, nil
	}

	// Convert to a string representation to avoid code repetition.
	representation, err := funcs.FormatInputType(t.TypeRef)
	return strings.Index(representation, "*") == 0, err
}

// formatName formats a GraphQL name (e.g. object, field, arg) into a Go equivalent
// Example: `fooId` -> `FooID`
func formatName(s string) string {
	if s == generator.QueryStructName {
		return generator.QueryStructClientName
	}
	if len(s) > 0 {
		s = strings.ToUpper(string(s[0])) + s[1:]
	}
	return lintName(s)
}

func formatIfaceImplName(s string) string {
	return strcase.ToLowerCamel(s) + "Impl"
}

// formatEnum formats a GraphQL Enum value into a Go equivalent
// Example: `fooId` -> `FooID`
func (funcs goTemplateFuncs) formatEnum(s string) string {
	s = strings.ToLower(s)
	return strcase.ToCamel(s)
}

func (funcs goTemplateFuncs) sortEnumFields(s []introspection.EnumValue) []introspection.EnumValue {
	sort.SliceStable(s, func(i, j int) bool {
		return s[i].Name < s[j].Name
	})
	return s
}

func (funcs goTemplateFuncs) formatArrayField(fields []*introspection.Field) string {
	result := []string{}

	for _, f := range fields {
		result = append(result, fmt.Sprintf("%s: &fields[i].%s", f.Name, funcs.ToUpperCase(f.Name)))
	}

	return strings.Join(result, ", ")
}

func (funcs goTemplateFuncs) formatArrayToSingleType(arrType string) string {
	return arrType[2:]
}

// fieldOptionsStructName returns the options struct name for a given field
func (funcs goTemplateFuncs) fieldOptionsStructName(f introspection.Field) string {
	// Exception: `Query` option structs are not prefixed by `Query`.
	// This is just so that they're nicer to work with, e.g.
	// `ContainerOpts` rather than `QueryContainerOpts`
	// The structure name will not clash with others since everybody else
	// is prefixed by object name.
	if f.ParentObject.Name == generator.QueryStructName {
		return formatName(f.Name) + "Opts"
	}
	return formatName(f.ParentObject.Name) + formatName(f.Name) + "Opts"
}

// fieldFunction converts a field into a function signature
// Example: `contents: String!` -> `func (r *File) Contents(ctx context.Context) (string, error)`
func (funcs goTemplateFuncs) fieldFunction(f introspection.Field) (string, error) {
	structName := formatName(f.ParentObject.Name)
	if f.ParentObject.Kind == introspection.TypeKindInterface {
		structName = formatIfaceImplName(structName)
	}
	signature := fmt.Sprintf(`func (r *%s) %s`,
		structName, formatName(f.Name))

	// Generate arguments
	args := []string{}
	if f.TypeRef.IsScalar() || f.TypeRef.IsList() {
		args = append(args, "ctx context.Context")
	}
	for _, arg := range f.Args {
		if arg.TypeRef.IsOptional() {
			continue
		}

		// FIXME: For top-level queries (e.g. File, Directory) if the field is named `id` then keep it as a
		// scalar (DirectoryID) rather than an object (*Directory).
		if f.ParentObject.Name == generator.QueryStructName && arg.Name == "id" {
			outType, err := funcs.FormatOutputType(arg.TypeRef)
			if err != nil {
				return "", fmt.Errorf("formatting output type: %w", err)
			}
			args = append(args, fmt.Sprintf("%s %s", arg.Name, outType))
		} else {
			inType, err := funcs.FormatInputType(arg.TypeRef)
			if err != nil {
				return "", fmt.Errorf("formatting input type: %w", err)
			}
			args = append(args, fmt.Sprintf("%s %s", arg.Name, inType))
		}
	}

	// Options (e.g. DirectoryContentsOptions -> <Object><Field>Options)
	if f.Args.HasOptionals() {
		args = append(
			args,
			fmt.Sprintf("opts ...%s", funcs.fieldOptionsStructName(f)),
		)
	}
	signature += "(" + strings.Join(args, ", ") + ")"

	retType, err := funcs.FormatReturnType(f)
	if err != nil {
		return "", fmt.Errorf("formatting return type: %w", err)
	}
	switch {
	case f.TypeRef.IsScalar(), f.TypeRef.IsList():
		retType = fmt.Sprintf("(%s, error)", retType)
	case f.TypeRef.IsInterface():
	default:
		retType = "*" + retType
	}
	signature += " " + retType

	return signature, nil
}

// TODO: doc
// TODO: dedupe w/ fieldFunction
func (funcs goTemplateFuncs) ifaceFunction(f introspection.Field) (string, error) {
	// skip ID field on interface definition
	if f.Name == "id" {
		return "", nil
	}

	signature := formatName(f.Name)

	// Generate arguments
	args := []string{}
	if f.TypeRef.IsScalar() || f.TypeRef.IsList() {
		args = append(args, "ctx context.Context")
	}
	for _, arg := range f.Args {
		if arg.TypeRef.IsOptional() {
			continue
		}

		// FIXME: For top-level queries (e.g. File, Directory) if the field is named `id` then keep it as a
		// scalar (DirectoryID) rather than an object (*Directory).
		if f.ParentObject.Name == generator.QueryStructName && arg.Name == "id" {
			outType, err := funcs.FormatOutputType(arg.TypeRef)
			if err != nil {
				return "", fmt.Errorf("formatting output type: %w", err)
			}
			args = append(args, fmt.Sprintf("%s %s", arg.Name, outType))
		} else {
			inType, err := funcs.FormatInputType(arg.TypeRef)
			if err != nil {
				return "", fmt.Errorf("formatting input type: %w", err)
			}
			args = append(args, fmt.Sprintf("%s %s", arg.Name, inType))
		}
	}

	// Options (e.g. DirectoryContentsOptions -> <Object><Field>Options)
	if f.Args.HasOptionals() {
		args = append(
			args,
			fmt.Sprintf("opts ...%s", funcs.fieldOptionsStructName(f)),
		)
	}
	signature += "(" + strings.Join(args, ", ") + ")"

	retType, err := funcs.FormatReturnType(f)
	if err != nil {
		return "", fmt.Errorf("formatting return type: %w", err)
	}
	switch {
	case f.TypeRef.IsScalar(), f.TypeRef.IsList():
		retType = fmt.Sprintf("(%s, error)", retType)
	case f.TypeRef.IsInterface():
	default:
		retType = "*" + retType
	}
	signature += " " + retType

	return signature, nil
}

func (funcs goTemplateFuncs) formatConcreteOutputType(r *introspection.TypeRef) (string, error) {
	name, err := funcs.FormatOutputType(r)
	if err != nil {
		return "", fmt.Errorf("formatting output type: %w", err)
	}
	if funcs.InnerType(r).IsInterface() {
		name = formatIfaceImplName(name)
	}
	return name, nil
}
