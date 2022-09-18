package router

import (
	"io"
	"net"
	"net/http"
	"sort"
	"sync"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/handler"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"go.dagger.io/dagger/playground"
)

const (
	DaggerContextEnv = "DAGGER_CONTEXT"
)

type Router struct {
	schemas      map[string]ExecutableSchema
	hostPlatform specs.Platform

	s *graphql.Schema
	h *handler.Handler
	l sync.RWMutex
}

func New(hostPlatform specs.Platform) *Router {
	r := &Router{
		schemas:      make(map[string]ExecutableSchema),
		hostPlatform: hostPlatform,
	}

	if err := r.Add(&rootSchema{}); err != nil {
		panic(err)
	}

	return r
}

func (r *Router) Add(schema ExecutableSchema) error {
	r.l.Lock()
	defer r.l.Unlock()

	// Copy the current schemas and append new schemas
	r.add(schema)
	newSchemas := []ExecutableSchema{}
	for _, s := range r.schemas {
		newSchemas = append(newSchemas, s)
	}
	sort.Slice(newSchemas, func(i, j int) bool {
		return newSchemas[i].Name() < newSchemas[j].Name()
	})

	merged, err := MergeExecutableSchemas("", newSchemas...)
	if err != nil {
		return err
	}

	s, err := compile(merged)
	if err != nil {
		return err
	}

	// Atomic swap
	r.s = s
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

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.l.RLock()
	curSchema := r.s
	r.l.RUnlock()

	defer func() {
		if v := recover(); v != nil {
			msg := "Internal Server Error"
			switch v := v.(type) {
			case error:
				msg = v.Error()
			case string:
				msg = v
			}
			http.Error(w, msg, http.StatusInternalServerError)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/query", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		sessionContext := SessionContext{
			Platform:     r.hostPlatform,
			HostPlatform: r.hostPlatform,
		}
		if marshalledContext := req.Header.Get(DaggerContextEnv); marshalledContext != "" {
			if err := sessionContext.UnmarshalText([]byte(marshalledContext)); err != nil {
				panic(err)
			}
		}
		handler.New(&handler.Config{
			Schema: curSchema,
		}).ContextHandler(initSessionContext(req.Context(), sessionContext), w, req)
	}))
	mux.Handle("/", playground.Handler("Cloak Dev", "/query"))
	mux.ServeHTTP(w, req)
}

func (r *Router) ServeConn(conn net.Conn) error {
	l := &singleConnListener{
		conn: conn,
	}

	return http.Serve(l, r)
}

// converts a pre-existing net.Conn into a net.Listener that returns the conn
type singleConnListener struct {
	conn net.Conn
	l    sync.Mutex
}

func (l *singleConnListener) Accept() (net.Conn, error) {
	l.l.Lock()
	defer l.l.Unlock()

	if l.conn == nil {
		return nil, io.ErrClosedPipe
	}
	c := l.conn
	l.conn = nil
	return c, nil
}

func (l *singleConnListener) Addr() net.Addr {
	return nil
}

func (l *singleConnListener) Close() error {
	return nil
}
