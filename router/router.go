package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"

	"github.com/dagger/dagger/internal/engine"
	"github.com/dagger/dagger/router/internal/handler"
	"github.com/dagger/graphql"
	"github.com/dagger/graphql/gqlerrors"
	"github.com/moby/buildkit/util/bklog"
	"github.com/vito/progrock"
)

type Router struct {
	schemas      map[string]ExecutableSchema
	resolvers    Resolvers
	sessionToken string

	recorder *progrock.Recorder

	s *graphql.Schema
	// mergedSchemaString is the merged schemas in SDL format, useful
	// for projects who need their dynamic schemas validated against
	// the router's current schema
	mergedSchemaString string
	h                  *handler.Handler
	l                  sync.RWMutex
}

func New(sessionToken string, recorder *progrock.Recorder) *Router {
	r := &Router{
		schemas:      make(map[string]ExecutableSchema),
		sessionToken: sessionToken,
		recorder:     recorder,
	}

	return r
}

func (r *Router) Add(schemas ...ExecutableSchema) error {
	r.l.Lock()
	defer r.l.Unlock()

	// Copy the current schemas and append new schemas
	for _, schema := range schemas {
		r.add(schema)
	}
	allSchemas := []ExecutableSchema{}
	for _, s := range r.schemas {
		allSchemas = append(allSchemas, s)
	}
	sort.Slice(allSchemas, func(i, j int) bool {
		return allSchemas[i].Name() < allSchemas[j].Name()
	})

	merged, err := MergeExecutableSchemas("", allSchemas...)
	if err != nil {
		return err
	}

	s, err := compile(merged)
	if err != nil {
		return err
	}

	// Atomic swap
	r.s = s
	r.resolvers = merged.Resolvers()
	r.mergedSchemaString = merged.Schema()
	r.h = handler.New(&handler.Config{
		Schema: s,
	})
	return nil
}

func (r *Router) add(schema ExecutableSchema) {
	// Skip adding schema if it has already been added, higher callers
	// are expected to handle checks that schemas with the same name are
	// actually equivalent
	_, ok := r.schemas[schema.Name()]
	if ok {
		return
	}

	r.schemas[schema.Name()] = schema
	for _, dep := range schema.Dependencies() {
		// TODO:(sipsma) guard against infinite recursion
		r.add(dep)
	}
}

func (r *Router) Get(name string) ExecutableSchema {
	r.l.RLock()
	defer r.l.RUnlock()

	return r.schemas[name]
}

func (r *Router) Resolvers() Resolvers {
	r.l.Lock()
	defer r.l.Unlock()
	return r.resolvers
}

func (r *Router) MergedSchemas() string {
	r.l.RLock()
	defer r.l.RUnlock()

	return r.mergedSchemaString
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.l.RLock()
	h := r.h
	r.l.RUnlock()

	w.Header().Add("x-dagger-engine", engine.Version)

	if r.sessionToken != "" {
		username, _, ok := req.BasicAuth()
		if !ok || username != r.sessionToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="Access to the Dagger engine session"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}

	defer func() {
		if v := recover(); v != nil {
			bklog.G(context.TODO()).Errorf("Router ServerHTTP: %+v", v)

			msg := "Internal Server Error"
			code := http.StatusInternalServerError
			switch v := v.(type) {
			case error:
				msg = v.Error()
				if errors.As(v, &InvalidInputError{}) {
					// panics can happen on invalid input in scalar serde
					code = http.StatusBadRequest
				}
			case string:
				msg = v
			}
			res := graphql.Result{
				Errors: []gqlerrors.FormattedError{
					gqlerrors.NewFormattedError(msg),
				},
			}
			bytes, err := json.Marshal(res)
			if err != nil {
				panic(err)
			}
			http.Error(w, string(bytes), code)
		}
	}()

	req = req.WithContext(progrock.RecorderToContext(req.Context(), r.recorder))
	req = req.WithContext(ContextWithSessionID(req.Context(), req.Header.Get(SessionIDHeader)))

	mux := http.NewServeMux()
	// TODO:
	// mux.Handle("/query", h)
	mux.Handle("/query", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		w.Write(rec.Body.Bytes())
		bklog.G(req.Context()).Debugf("Router: %s %s %d %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
	}))
	mux.ServeHTTP(w, req)
}

const SessionIDHeader = "X-Dagger-Session-ID"

type contextSessionIDKey struct{}

func ContextWithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, contextSessionIDKey{}, sessionID)
}

func SessionIDFromContext(ctx context.Context) string {
	sessionID, _ := ctx.Value(contextSessionIDKey{}).(string)
	return sessionID
}

func EngineConn(r http.Handler) DirectConn {
	return func(req *http.Request) (*http.Response, error) {
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, req)
		return resp.Result(), nil
	}
}

type DirectConn func(*http.Request) (*http.Response, error)

func (f DirectConn) Do(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (f DirectConn) Host() string {
	return ":mem:"
}

func (f DirectConn) Close() error {
	return nil
}
