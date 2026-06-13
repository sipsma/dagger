package core

import (
	"context"
	"fmt"

	"github.com/dagger/dagger/dagql"
)

func currentExecutionPrincipal(ctx context.Context, label string) (dagql.ExecutionPrincipal, error) {
	principal, err := dagql.ExecutionPrincipalFromContext(ctx)
	if err != nil {
		return dagql.ExecutionPrincipal{}, fmt.Errorf("%s: current execution principal: %w", label, err)
	}
	return principal, nil
}
