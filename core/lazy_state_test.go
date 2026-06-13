package core

import (
	"context"
	"errors"
	"testing"

	"github.com/dagger/dagger/dagql"
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
