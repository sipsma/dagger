package dagql

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/util/hashutil"

	"github.com/dagger/dagger/engine"
)

// CachePerClientInput scopes a call ID per client by mixing in the client ID as
// an implicit call input.
var CachePerClientInput = ImplicitInput{
	Name: "cachePerClient",
	Resolver: func(ctx context.Context, _ map[string]Input) (Input, error) {
		clientMD, err := engine.ClientMetadataFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get client metadata: %w", err)
		}
		if clientMD.ClientID == "" {
			return nil, fmt.Errorf("client ID not found in context")
		}
		return NewString(clientMD.ClientID), nil
	},
}

// CachePerSessionInput scopes a call ID per session by mixing in the session
// ID as an implicit call input.
var CachePerSessionInput = ImplicitInput{
	Name: "cachePerSession",
	Resolver: func(ctx context.Context, _ map[string]Input) (Input, error) {
		clientMD, err := engine.ClientMetadataFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get client metadata: %w", err)
		}
		if clientMD.SessionID == "" {
			return nil, fmt.Errorf("session ID not found in context")
		}
		return NewString(clientMD.SessionID), nil
	},
}

// CachePerCallInput scopes a call ID per invocation by mixing in a random value
// as an implicit call input.
var CachePerCallInput = ImplicitInput{
	Name: "cachePerCall",
	Resolver: func(context.Context, map[string]Input) (Input, error) {
		return NewString(identity.NewID()), nil
	},
}

// CachePerSchemaInput scopes a call ID to the server schema digest.
func CachePerSchemaInput(srv *Server) ImplicitInput {
	return ImplicitInput{
		Name: "cachePerSchema",
		Resolver: func(context.Context, map[string]Input) (Input, error) {
			return NewString(srv.SchemaDigest().String()), nil
		},
	}
}

// CacheAsRequestedInput scopes a call ID according to a boolean argument:
// false => CachePerClientInput, true => CachePerCallInput.
func CacheAsRequestedInput(argName string) ImplicitInput {
	return ImplicitInput{
		Name: "cacheAsRequested:" + argName,
		Resolver: func(ctx context.Context, args map[string]Input) (Input, error) {
			noCache, err := inputBoolArg(args, argName)
			if err != nil {
				return nil, err
			}
			if noCache {
				return CachePerCallInput.Resolver(ctx, args)
			}
			return CachePerClientInput.Resolver(ctx, args)
		},
	}
}

func inputBoolArg(args map[string]Input, argName string) (bool, error) {
	raw, ok := args[argName]
	if !ok || raw == nil {
		return false, nil
	}
	switch val := raw.(type) {
	case Boolean:
		return val.Bool(), nil
	case Optional[Boolean]:
		if !val.Valid {
			return false, nil
		}
		return val.Value.Bool(), nil
	default:
		return false, fmt.Errorf("cacheAsRequested input %q must be Boolean, got %T", argName, raw)
	}
}

// CachePerClient is a CacheKeyFunc that scopes the cache key to the client by mixing in the client ID to the original digest of the operation.
// It should be used when the operation should be run for each client, but not more than once for a given client.
// Canonical examples include loading client filesystem data or referencing client-side sockets/ports.
func CachePerClient[P Typed, A any](
	ctx context.Context,
	inst ObjectResult[P],
	args A,
	req GetCacheConfigRequest,
) (*GetCacheConfigResponse, error) {
	return CachePerClientObject(ctx, inst, args, req)
}

// CachePerClientObject is the same as CachePerClient but when you have a dagql.Object instead of a dagql.Result.
func CachePerClientObject[A any](
	ctx context.Context,
	_ AnyObjectResult,
	_ A,
	req GetCacheConfigRequest,
) (*GetCacheConfigResponse, error) {
	clientMD, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get client metadata: %w", err)
	}
	if clientMD.ClientID == "" {
		return nil, fmt.Errorf("client ID not found in context")
	}

	resp := &GetCacheConfigResponse{
		CacheKey: req.CacheKey,
	}
	resp.CacheKey.CallKey = hashutil.HashStrings(resp.CacheKey.CallKey, clientMD.ClientID).String()
	return resp, nil
}

// CachePerSession is a CacheKeyFunc that scopes the cache key to the session by mixing in the session ID to the original digest of the operation.
// It should be used when the operation should be run for each session, but not more than once for a given session.
func CachePerSession[P Typed, A any](
	ctx context.Context,
	inst ObjectResult[P],
	args A,
	req GetCacheConfigRequest,
) (*GetCacheConfigResponse, error) {
	return CachePerSessionObject(ctx, inst, args, req)
}

// CachePerSessionObject is the same as CachePerSession but when you have a dagql.Object instead of a dagql.Result.
func CachePerSessionObject[A any](
	ctx context.Context,
	_ AnyObjectResult,
	_ A,
	req GetCacheConfigRequest,
) (*GetCacheConfigResponse, error) {
	clientMD, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get client metadata: %w", err)
	}
	if clientMD.SessionID == "" {
		return nil, fmt.Errorf("session ID not found in context")
	}

	resp := &GetCacheConfigResponse{
		CacheKey: req.CacheKey,
	}
	resp.CacheKey.CallKey = hashutil.HashStrings(resp.CacheKey.CallKey, clientMD.SessionID).String()
	return resp, nil
}

// this could all be un-generic'd and repeated per-API. might be cleaner at the end of the day.
type CacheControllableArgs interface {
	CacheType() CacheControlType
}

type CacheControlType int

const (
	CacheTypeUnset CacheControlType = iota
	CacheTypePerClient
	CacheTypePerCall
)

func CacheAsRequested[T Typed, A CacheControllableArgs](ctx context.Context, i ObjectResult[T], a A, req GetCacheConfigRequest) (*GetCacheConfigResponse, error) {
	switch a.CacheType() {
	case CacheTypePerClient:
		return CachePerClient(ctx, i, a, req)
	case CacheTypePerCall:
		return CachePerCall(ctx, i, a, req)
	default:
		resp := &GetCacheConfigResponse{CacheKey: req.CacheKey}
		return resp, nil
	}
}

// CachePerCall results in the API always running when called, but the returned result from that call is cached.
// For instance, the API may return a snapshot of some live mutating state; in that case the first call to get the snapshot
// should always run but if the returned object is passed around it should continue to be that snapshot rather than the API
// always re-running.
func CachePerCall[P Typed, A any](
	_ context.Context,
	_ ObjectResult[P],
	_ A,
	req GetCacheConfigRequest,
) (*GetCacheConfigResponse, error) {
	randID := identity.NewID()
	resp := &GetCacheConfigResponse{CacheKey: req.CacheKey}
	resp.CacheKey.CallKey = randID
	return resp, nil
}

// CachePerSchema is a CacheKeyFunc that scopes the cache key to the schema of
// the provided server.
//
// This should be used only in scenarios where literally the schema is all that
// determines the result, irrespective of what client is making the call.
func CachePerSchema[P Typed, A any](srv *Server) func(context.Context, ObjectResult[P], A, GetCacheConfigRequest) (*GetCacheConfigResponse, error) {
	return func(
		ctx context.Context,
		_ ObjectResult[P],
		_ A,
		req GetCacheConfigRequest,
	) (*GetCacheConfigResponse, error) {
		resp := &GetCacheConfigResponse{CacheKey: req.CacheKey}
		schemaDgst := srv.SchemaDigest()
		resp.CacheKey.CallKey = hashutil.HashStrings(
			resp.CacheKey.CallKey,
			schemaDgst.String(),
		).String()
		return resp, nil
	}
}

// CachePerClientSchema is a CacheKeyFunc that scopes the cache key to both the
// client and the current schema of the provided server.
//
// This should be used by anything that should invalidate when the schema
// changes, but also has an element of per-client dynamism.
func CachePerClientSchema[P Typed, A any](srv *Server) func(context.Context, ObjectResult[P], A, GetCacheConfigRequest) (*GetCacheConfigResponse, error) {
	return func(
		ctx context.Context,
		_ ObjectResult[P],
		_ A,
		req GetCacheConfigRequest,
	) (*GetCacheConfigResponse, error) {
		clientMD, err := engine.ClientMetadataFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get client metadata: %w", err)
		}
		if clientMD.ClientID == "" {
			return nil, fmt.Errorf("client ID not found in context")
		}

		resp := &GetCacheConfigResponse{CacheKey: req.CacheKey}
		resp.CacheKey.CallKey = hashutil.HashStrings(
			resp.CacheKey.CallKey,
			srv.SchemaDigest().String(),
			clientMD.ClientID,
		).String()
		return resp, nil
	}
}
