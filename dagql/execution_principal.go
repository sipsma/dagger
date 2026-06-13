package dagql

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/engine"
)

// ExecutionPrincipal identifies the client session that is responsible for
// deferred engine work such as lazy evaluation.
type ExecutionPrincipal struct {
	SessionID string
	ClientID  string
}

type executionPrincipalCtxKey struct{}

func ContextWithExecutionPrincipal(ctx context.Context, principal ExecutionPrincipal) context.Context {
	return context.WithValue(ctx, executionPrincipalCtxKey{}, principal)
}

func ExecutionPrincipalFromContext(ctx context.Context) (ExecutionPrincipal, error) {
	if principal, ok := ctx.Value(executionPrincipalCtxKey{}).(ExecutionPrincipal); ok {
		if principal.SessionID == "" {
			return ExecutionPrincipal{}, fmt.Errorf("execution principal: empty session ID")
		}
		if principal.ClientID == "" {
			return ExecutionPrincipal{}, fmt.Errorf("execution principal: empty client ID")
		}
		return principal, nil
	}

	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return ExecutionPrincipal{}, err
	}
	if clientMetadata.SessionID == "" {
		return ExecutionPrincipal{}, fmt.Errorf("execution principal: empty session ID")
	}
	if clientMetadata.ClientID == "" {
		return ExecutionPrincipal{}, fmt.Errorf("execution principal: empty client ID")
	}
	return ExecutionPrincipal{
		SessionID: clientMetadata.SessionID,
		ClientID:  clientMetadata.ClientID,
	}, nil
}
