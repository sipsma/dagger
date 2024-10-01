package main

import (
	"context"
)

type Test struct{}

func (m *Test) Foo(ctx context.Context, bar string) error {
	_, err := dag.Container().
		From("alpine:latest").
		WithExec([]string{"echo", bar}).
		Sync(ctx)
	return err
}
