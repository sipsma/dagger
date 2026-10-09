package server

// DIAGNOSTIC ONLY (DO NOT MERGE): splits session.query's own time into
// q.read (body read + JSON decode), q.parseValidate (parse, validate,
// variables) and q.write (from the response handler returning to ServeHTTP
// returning) via gqlgen extension hooks.

import (
	"context"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/dagger/dagger/engine/wcprof"
)

type queryProfMarks struct {
	start, params, opCtx, resp int64
}

type queryProfMarksKey struct{}

func withQueryProfMarks(ctx context.Context) (context.Context, *queryProfMarks) {
	m := &queryProfMarks{start: wcprof.NowNS()}
	return context.WithValue(ctx, queryProfMarksKey{}, m), m
}

func queryProfMarksFrom(ctx context.Context) *queryProfMarks {
	m, _ := ctx.Value(queryProfMarksKey{}).(*queryProfMarks)
	return m
}

type queryProfExt struct{}

var (
	_ graphql.HandlerExtension          = queryProfExt{}
	_ graphql.OperationParameterMutator = queryProfExt{}
	_ graphql.OperationContextMutator   = queryProfExt{}
	_ graphql.ResponseInterceptor       = queryProfExt{}
)

func (queryProfExt) ExtensionName() string                   { return "wcprofQueryPhases" }
func (queryProfExt) Validate(graphql.ExecutableSchema) error { return nil }
func (queryProfExt) MutateOperationParameters(ctx context.Context, _ *graphql.RawParams) *gqlerror.Error {
	if m := queryProfMarksFrom(ctx); m != nil {
		m.params = wcprof.NowNS()
	}
	return nil
}

func (queryProfExt) MutateOperationContext(ctx context.Context, _ *graphql.OperationContext) *gqlerror.Error {
	if m := queryProfMarksFrom(ctx); m != nil {
		m.opCtx = wcprof.NowNS()
	}
	return nil
}

func (queryProfExt) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	res := next(ctx)
	if m := queryProfMarksFrom(ctx); m != nil {
		m.resp = wcprof.NowNS()
	}
	return res
}

func useQueryProf(srv *handler.Server) {
	srv.Use(queryProfExt{})
}

// recordQueryProfMarks records the phases as children of the session.query
// op in ctx, skipping any whose boundary was not reached.
func recordQueryProfMarks(ctx context.Context, m *queryProfMarks, clientID string) {
	end := wcprof.NowNS()
	if m == nil || m.start == 0 {
		return
	}
	opts := wcprof.OpOpts{ClientID: clientID}
	if m.params != 0 {
		wcprof.RecordOp(ctx, wcprof.OpKindSessionPhase, "q.read", opts, m.start, m.params, wcprof.OutcomeOK)
		if m.opCtx != 0 {
			wcprof.RecordOp(ctx, wcprof.OpKindSessionPhase, "q.parseValidate", opts, m.params, m.opCtx, wcprof.OutcomeOK)
		}
	}
	if m.resp != 0 {
		wcprof.RecordOp(ctx, wcprof.OpKindSessionPhase, "q.write", opts, m.resp, end, wcprof.OutcomeOK)
	}
}
