package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/graphql-go/graphql"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type LoadedSchema interface {
	Name() string
	Schema() string
}

type ExecutableSchema interface {
	LoadedSchema
	Resolvers() Resolvers
	Dependencies() []ExecutableSchema
}

type Resolvers map[string]Resolver

type Resolver interface {
	_resolver()
}

type ObjectResolver map[string]graphql.FieldResolveFn

func (ObjectResolver) _resolver() {}

type ScalarResolver struct {
	Serialize    graphql.SerializeFn
	ParseValue   graphql.ParseValueFn
	ParseLiteral graphql.ParseLiteralFn
}

func (ScalarResolver) _resolver() {}

type StaticSchemaParams struct {
	Name         string
	Schema       string
	Resolvers    Resolvers
	Dependencies []ExecutableSchema
}

func StaticSchema(p StaticSchemaParams) ExecutableSchema {
	return &staticSchema{p}
}

var _ ExecutableSchema = &staticSchema{}

type staticSchema struct {
	StaticSchemaParams
}

func (s *staticSchema) Name() string {
	return s.StaticSchemaParams.Name
}

func (s *staticSchema) Schema() string {
	return s.StaticSchemaParams.Schema
}

func (s *staticSchema) Resolvers() Resolvers {
	return s.StaticSchemaParams.Resolvers
}

func (s *staticSchema) Dependencies() []ExecutableSchema {
	return s.StaticSchemaParams.Dependencies
}

type SessionContext struct {
	Platform specs.Platform
}

func (c SessionContext) MarshalText() ([]byte, error) {
	type marshaler SessionContext // avoid infinite recursion with json.Marshal using MarshalText
	jsonBytes, err := json.Marshal(marshaler(c))
	if err != nil {
		panic(err)
	}
	b64Bytes := make([]byte, base64.StdEncoding.EncodedLen(len(jsonBytes)))
	base64.StdEncoding.Encode(b64Bytes, jsonBytes)
	return b64Bytes, nil
}

func (c *SessionContext) UnmarshalText(text []byte) error {
	jsonBytes := make([]byte, base64.StdEncoding.DecodedLen(len(text)))
	n, err := base64.StdEncoding.Decode(jsonBytes, text)
	if err != nil {
		return err
	}
	type unmarshaler SessionContext // avoid infinite recursion
	return json.Unmarshal(jsonBytes[:n], (*unmarshaler)(c))
}

type sessionContextKey struct{}

func initSessionContext(ctx context.Context, sessCtx SessionContext) context.Context {
	m := &sync.Map{}
	m.Store("", sessCtx)
	return context.WithValue(ctx, sessionContextKey{}, m)
}

func SessionContextOf(p graphql.ResolveParams) (SessionContext, error) {
	m, ok := p.Context.Value(sessionContextKey{}).(*sync.Map)
	if !ok {
		return SessionContext{}, fmt.Errorf("no session context map found in context")
	}

	// The p.Context object is shared between all independent paths in a query and also
	// doesn't support directly changing values (e.g. via context.WithValue). So we instead
	// store a sync.Map in the context (initialized in router.go) and then keep track of the
	// session context in that map, using paths as a key.
	//
	// We lookup the session context by checking the current path's parent. If it's not set
	// then we keep checking the parent's parent, etc. until we find a session context. This
	// could be much more efficient, but it's unexpected to matter at the moment.
	path := p.Info.Path.AsArray()
	for i := len(path); i >= 0; i-- {
		var fields []string
		for _, v := range path[:i] {
			fields = append(fields, v.(string))
		}
		key := strings.Join(fields, ".")

		sessCtx, ok := m.Load(key)
		if ok {
			return sessCtx.(SessionContext), nil
		}
	}
	return SessionContext{}, fmt.Errorf("no session context found for path %+v", path)
}

func PlatformOf(p graphql.ResolveParams) (specs.Platform, error) {
	sessionCtx, err := SessionContextOf(p)
	if err != nil {
		return specs.Platform{}, err
	}
	return sessionCtx.Platform, nil
}

func SetSessionContext(p graphql.ResolveParams, sessCtx SessionContext) error {
	m, ok := p.Context.Value(sessionContextKey{}).(*sync.Map)
	if !ok {
		return fmt.Errorf("no session context map found in context")
	}

	path := p.Info.Path.AsArray()
	fields := make([]string, 0, len(path))
	for _, v := range path {
		fields = append(fields, v.(string))
	}
	key := strings.Join(fields, ".")

	m.Store(key, sessCtx)
	return nil
}
