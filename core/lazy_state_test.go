package core

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/dagger/dagger/dagql"
	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
)

type lazyAccessorTestMaterializer[V any, T dagql.Typed] struct {
	value V
	ok    bool
	err   error
	calls int
}

func (m *lazyAccessorTestMaterializer[V, T]) Materialize(context.Context, dagql.Result[T]) (V, bool, error) {
	m.calls++
	return m.value, m.ok, m.err
}

type blockingLazyAccessorMaterializer struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (m *blockingLazyAccessorMaterializer) Materialize(context.Context, dagql.Result[*Directory]) (int, bool, error) {
	if m.calls.Add(1) == 1 {
		close(m.started)
		<-m.release
		return 42, true, nil
	}
	return 99, true, nil
}

func TestLazyAccessorGetOrEvalReturnsSetValueWithoutCache(t *testing.T) {
	t.Parallel()

	var accessor LazyAccessor[int, *Directory]
	accessor.setValue(42)

	got, err := accessor.GetOrEval(context.Background(), dagql.Result[*Directory]{})
	assert.NilError(t, err)
	assert.Equal(t, got, 42)
}

func TestLazyAccessorGetOrEvalUsesMaterializerBeforeCacheEval(t *testing.T) {
	t.Parallel()

	materializer := &lazyAccessorTestMaterializer[int, *Directory]{
		value: 42,
		ok:    true,
	}
	var accessor LazyAccessor[int, *Directory]
	accessor.setMaterializer(materializer)

	got, err := accessor.GetOrEval(context.Background(), dagql.Result[*Directory]{})
	assert.NilError(t, err)
	assert.Equal(t, got, 42)
	assert.Equal(t, materializer.calls, 1)
}

func TestLazyAccessorGetOrEvalSerializesConcurrentMaterializerSuccess(t *testing.T) {
	t.Parallel()

	materializer := &blockingLazyAccessorMaterializer{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	var accessor LazyAccessor[int, *Directory]
	accessor.setMaterializer(materializer)

	callGate := make(chan struct{})
	ready := make(chan struct{}, 2)
	eg, egCtx := errgroup.WithContext(context.Background())
	for i := 0; i < 2; i++ {
		eg.Go(func() error {
			ready <- struct{}{}
			<-callGate
			got, err := accessor.GetOrEval(egCtx, dagql.Result[*Directory]{})
			if err != nil {
				return err
			}
			if got != 42 {
				return fmt.Errorf("got %d, want 42", got)
			}
			return nil
		})
	}
	<-ready
	<-ready
	close(callGate)
	<-materializer.started
	close(materializer.release)

	assert.NilError(t, eg.Wait())
	assert.Equal(t, materializer.calls.Load(), int32(1))
}

func TestLazyAccessorGetOrEvalFallsBackWhenMaterializerDeclines(t *testing.T) {
	t.Parallel()

	materializer := &lazyAccessorTestMaterializer[int, *Directory]{}
	var accessor LazyAccessor[int, *Directory]
	accessor.setMaterializer(materializer)

	_, err := accessor.GetOrEval(context.Background(), dagql.Result[*Directory]{})
	assert.ErrorContains(t, err, "no dagql cache in context")
	assert.Equal(t, materializer.calls, 1)
}

func TestLazyAccessorGetOrEvalReturnsMaterializerHardError(t *testing.T) {
	t.Parallel()

	expected := errors.New("materializer failed")
	materializer := &lazyAccessorTestMaterializer[int, *Directory]{
		err: expected,
	}
	var accessor LazyAccessor[int, *Directory]
	accessor.setMaterializer(materializer)

	_, err := accessor.GetOrEval(context.Background(), dagql.Result[*Directory]{})
	assert.ErrorIs(t, err, expected)
	assert.Equal(t, materializer.calls, 1)
}

func TestLazyAccessorGetOrEvalWithoutPlanPreservesOldCacheEvalPath(t *testing.T) {
	t.Parallel()

	var accessor LazyAccessor[int, *Directory]

	_, err := accessor.GetOrEval(context.Background(), dagql.Result[*Directory]{})
	assert.ErrorContains(t, err, "no dagql cache in context")
}
