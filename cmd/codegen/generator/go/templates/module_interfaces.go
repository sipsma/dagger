package templates

import (
	"fmt"
	"go/types"
	"sort"
	"strings"

	. "github.com/dave/jennifer/jen" // nolint:revive,stylecheck
	"github.com/iancoleman/strcase"
)

func (ps *parseState) parseGoIface(t *types.Interface, named *types.Named) (*parsedIfaceType, error) {
	spec := &parsedIfaceType{
		goType:     t,
		moduleName: ps.moduleName,
	}

	if named == nil {
		return nil, fmt.Errorf("struct types must be named")
	}
	spec.name = named.Obj().Name()
	if spec.name == "" {
		return nil, fmt.Errorf("struct types must be named")
	}

	// It's safe to compare objects directly: https://github.com/golang/example/tree/1d6d2400d4027025cb8edc86a139c9c581d672f7/gotypes#objects
	// (search "objects are routinely compared by the addresses of the underlying pointers")
	daggerObjectIfaceMethods := make(map[types.Object]bool)
	daggerObjectMethodSet := types.NewMethodSet(ps.daggerObjectIfaceType)
	for i := 0; i < daggerObjectMethodSet.Len(); i++ {
		daggerObjectIfaceMethods[daggerObjectMethodSet.At(i).Obj()] = false
	}

	goFuncTypes := []*types.Func{}
	methodSet := types.NewMethodSet(named)
	for i := 0; i < methodSet.Len(); i++ {
		methodObj := methodSet.At(i).Obj()

		// check if this is a method from the embedded DaggerObject interface
		if _, ok := daggerObjectIfaceMethods[methodObj]; ok {
			daggerObjectIfaceMethods[methodObj] = true
			continue
		}

		goFuncType, ok := methodObj.(*types.Func)
		if !ok {
			return nil, fmt.Errorf("expected method to be a func, got %T", methodObj)
		}

		if !goFuncType.Exported() {
			continue
		}

		goFuncTypes = append(goFuncTypes, goFuncType)
	}
	// verify the DaggerObject interface methods are all there
	for methodObj, found := range daggerObjectIfaceMethods {
		if !found {
			return nil, fmt.Errorf("missing method %s from DaggerObject interface, which must be embedded in interfaces used in Functions and Objects", methodObj.Name())
		}
	}

	sort.Slice(goFuncTypes, func(i, j int) bool {
		return goFuncTypes[i].Pos() < goFuncTypes[j].Pos()
	})
	for _, goFuncType := range goFuncTypes {
		funcTypeSpec, err := ps.parseGoFunc(named, goFuncType)
		if err != nil {
			return nil, fmt.Errorf("failed to parse method %s: %w", goFuncType.Name(), err)
		}
		spec.methods = append(spec.methods, funcTypeSpec)
	}

	// get the comment above the interface (if any)
	astSpec, err := ps.astSpecForNamedType(named)
	if err != nil {
		return nil, fmt.Errorf("failed to find decl for named type %s: %w", spec.name, err)
	}
	spec.doc = astSpec.Doc.Text()

	return spec, nil
}

type parsedIfaceType struct {
	name string
	doc  string

	methods []*funcTypeSpec

	goType     *types.Interface
	moduleName string
}

var _ ParsedType = &parsedIfaceType{}

func (spec *parsedIfaceType) TypeDefCode() (*Statement, error) {
	withIfaceArgsCode := []Code{
		Lit(spec.name),
	}
	withIfaceOptsCode := []Code{}
	if spec.doc != "" {
		withIfaceOptsCode = append(withIfaceOptsCode, Id("Description").Op(":").Lit(strings.TrimSpace(spec.doc)))
	}
	if len(withIfaceOptsCode) > 0 {
		withIfaceArgsCode = append(withIfaceArgsCode, Id("TypeDefWithInterfaceOpts").Values(withIfaceOptsCode...))
	}

	typeDefCode := Qual("dag", "TypeDef").Call().Dot("WithInterface").Call(withIfaceArgsCode...)

	for _, method := range spec.methods {
		fnTypeDefCode, err := method.TypeDefCode()
		if err != nil {
			return nil, fmt.Errorf("failed to convert method %s to function def: %w", method.name, err)
		}
		typeDefCode = dotLine(typeDefCode, "WithFunction").Call(Add(Line(), fnTypeDefCode))
	}

	return typeDefCode, nil
}

func (spec *parsedIfaceType) GoType() types.Type {
	return spec.goType
}

func (spec *parsedIfaceType) GoSubTypes() []types.Type {
	var subTypes []types.Type
	for _, method := range spec.methods {
		subTypes = append(subTypes, method.GoSubTypes()...)
	}
	return subTypes
}

func (spec *parsedIfaceType) ConcreteStructCode() ([]Code, error) {
	structName := formatIfaceImplName(spec.name)
	idTypeName := spec.name + "ID"
	loadFromIDMethodName := fmt.Sprintf("Load%sFromID", spec.name)
	// TODO: the fact that we have to account for namespacing here is not ideal...
	loadFromIDQueryName := fmt.Sprintf("load%s%sFromID", strcase.ToCamel(spec.moduleName), spec.name)

	structDefCode := Type().Id(structName).StructFunc(func(g *Group) {
		g.Id("q").Op("*").Qual("querybuilder", "Selection")
		g.Id("c").Qual("graphql", "Client")
		g.Id("id").Op("*").Id(idTypeName)

		for _, method := range spec.methods {
			if method.returnSpec == nil {
				continue
			}
			primitiveType, ok := method.returnSpec.(*parsedPrimitiveType)
			if !ok {
				continue
			}
			g.Id(strcase.ToLowerCamel(method.name)).Op("*").Id(primitiveType.GoType().String())
		}
	})

	idDefCode := Type().Id(idTypeName).String()

	loadFromIDMethodCode := Func().Params(Id("r").Op("*").Id("Client")).
		Id(loadFromIDMethodName).
		Params(Id("id").Id(idTypeName)).
		Params(Id(spec.name)).
		BlockFunc(func(g *Group) {
			g.Id("q").Op(":=").Id("r").Dot("q").Dot("Select").Call(Lit(loadFromIDQueryName))
			g.Id("q").Op("=").Id("q").Dot("Arg").Call(Lit("id"), Id("id"))
			g.Return(Op("&").Id(structName).Values(Dict{
				Id("q"): Id("q"),
				Id("c"): Id("r").Dot("c"),
			}))
		})

	genQueryScalarMethodCode := func(methodName, structFieldName, returnTypeName string) *Statement {
		return Func().Params(Id("r").Op("*").Id(structName)).
			Id(methodName).
			Params(Id("ctx").Qual("context", "Context")).
			Params(Id(returnTypeName), Id("error")).
			BlockFunc(func(g *Group) {
				g.If(Id("r").Dot(structFieldName).Op("!=").Nil()).Block(
					Return(Op("*").Id("r").Dot(structFieldName), Nil()),
				)
				g.Id("q").Op(":=").Id("r").Dot("q").Dot("Select").Call(Lit(structFieldName))
				g.Var().Id("response").Id(returnTypeName)
				g.Id("q").Op("=").Id("q").Dot("Bind").Call(Op("&").Id("response"))
				g.Return(Id("response"), Id("q").Dot("Execute").Call(Id("ctx"), Id("r").Dot("c")))
			})
	}

	methodCodes := []Code{
		genQueryScalarMethodCode("ID", "id", idTypeName),
	}
	for _, method := range spec.methods {
		methodCodes = append(methodCodes, genQueryScalarMethodCode(
			method.name,
			strcase.ToLowerCamel(method.name),
			method.returnSpec.GoType().String(),
		))
	}

	// XXX_* methods
	methodCodes = append(methodCodes, Func().Params(Id("r").Op("*").Id(structName)).
		Id("XXX_GraphQLType").
		Params().
		Params(Id("string")).
		Block(Return(Lit(spec.name))),
	)
	methodCodes = append(methodCodes, Func().Params(Id("r").Op("*").Id(structName)).
		Id("XXX_GraphQLIDType").
		Params().
		Params(Id("string")).
		Block(Return(Lit(idTypeName))),
	)
	methodCodes = append(methodCodes, Func().Params(Id("r").Op("*").Id(structName)).
		Id("XXX_GraphQLID").
		Params(Id("ctx").Qual("context", "Context")).
		Params(Id("string"), Id("error")).
		BlockFunc(func(g *Group) {
			g.List(Id("id"), Id("err")).Op(":=").Id("r").Dot("ID").Call(Id("ctx"))
			g.If(Id("err").Op("!=").Nil()).Block(Return(Lit(""), Id("err")))
			g.Return(Id("string").Parens(Id("id")), Nil())
		}),
	)

	// JSON (un)marshal methods
	methodCodes = append(methodCodes, Func().Params(Id("r").Op("*").Id(structName)).
		Id("MarshalJSON").
		Params().
		Params(Id("[]byte"), Id("error")).
		BlockFunc(func(g *Group) {
			g.List(Id("id"), Id("err")).Op(":=").Id("r").Dot("ID").Call(Qual("context", "Background").Call())
			g.If(Id("err").Op("!=").Nil()).Block(Return(Nil(), Id("err")))
			g.Return(Id("json").Dot("Marshal").Call(Id("id")))
		}),
	)
	methodCodes = append(methodCodes, Func().Params(Id("r").Op("*").Id(structName)).
		Id("UnmarshalJSON").
		Params(Id("bs").Id("[]byte")).
		Params(Id("error")).
		BlockFunc(func(g *Group) {
			g.Var().Id("id").Id(idTypeName)
			g.Id("err").Op(":=").Id("json").Dot("Unmarshal").Call(Id("bs"), Op("&").Id("id"))
			g.If(Id("err").Op("!=").Nil()).Block(Return(Id("err")))
			g.Op("*").Id("r").Op("=").Op("*").Id("dag").Dot(loadFromIDMethodName).Call(Id("id")).Assert(Id("*").Id(structName))
			g.Return(Nil())
		}),
	)

	// convert to iface method
	methodCodes = append(methodCodes, Func().Params(Id("r").Id(structName)).
		Id("toIface").
		Params().
		Params(Id(spec.name)).
		BlockFunc(func(g *Group) {
			g.Return(Op("&").Id("r"))
		}),
	)

	allCode := []Code{
		structDefCode,
		idDefCode,
		loadFromIDMethodCode,
	}
	allCode = append(allCode, methodCodes...)
	return allCode, nil
}
