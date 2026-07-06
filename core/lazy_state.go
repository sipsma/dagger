package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/slog"
)

type Lazy[T dagql.Typed] interface {
	Evaluate(context.Context, T) error
	AttachDependencies(context.Context, func(dagql.AnyResult) (dagql.AnyResult, error)) ([]dagql.AnyResult, error)
	EncodePersisted(context.Context, dagql.PersistedObjectCache) (json.RawMessage, error)
}

type LazyState struct {
	LazyMu           *sync.Mutex
	LazyInitComplete bool
}

func NewLazyState() LazyState {
	return LazyState{
		LazyMu: new(sync.Mutex),
	}
}

func (lazy *LazyState) Evaluate(ctx context.Context, typeName string, run func(context.Context) error) (rerr error) {
	if lazy.LazyInitComplete {
		return nil
	}
	if run == nil {
		lazy.LazyInitComplete = true
		return nil
	}

	if lazy.LazyMu == nil {
		return fmt.Errorf("invalid %s: missing LazyMu", typeName)
	}

	lazy.LazyMu.Lock()
	defer lazy.LazyMu.Unlock()

	if lazy.LazyInitComplete {
		return nil
	}

	start := time.Now()
	slog.InfoContext(ctx, "start lazy evaluation",
		"field", typeName,
	)
	defer func() {
		args := []any{
			"field", typeName,
			"duration", time.Since(start),
		}
		if rerr != nil {
			args = append(args, "err", rerr)
		}
		slog.InfoContext(ctx, "end lazy evaluation", args...)
	}()

	if rerr = run(ctx); rerr != nil {
		return rerr
	}
	lazy.LazyInitComplete = true
	return nil
}

type LazyAccessor[V any, T dagql.Typed] struct {
	value V // should not be gotten/set directly except for actual evaluation implementations!
	isSet bool
	mu    sync.RWMutex

	// fill maps the owner type's canonical value to the field this accessor
	// mirrors. It lets GetOrEval resolve an accessor that was copied from a
	// value before that value realized: the canonical value holds the
	// content, and this copy only lost its local pointer to it.
	fill func(T) (V, bool)
}

// NewLazyAccessor constructs an accessor whose empty state can be resolved
// from the owner type's canonical value via the given fill function.
func NewLazyAccessor[V any, T dagql.Typed](fill func(T) (V, bool)) *LazyAccessor[V, T] {
	return &LazyAccessor[V, T]{fill: fill}
}

// WARN: res MUST be the dagql result wrapper whose canonical value this
// accessor mirrors — either the accessor is on that canonical value itself,
// or it is a copy of that value's accessor. The accessor cannot validate
// that today due to the current Directory/File/Container vs dagql.Result
// split, so callers must pass the matching result explicitly and carefully.
func (a *LazyAccessor[V, T]) GetOrEval(ctx context.Context, res dagql.Result[T]) (V, error) {
	var zero V

	c, err := dagql.EngineCache(ctx)
	if err != nil {
		return zero, err
	}
	err = c.Evaluate(ctx, res)
	if err != nil {
		return zero, err
	}

	// evaluate should have set our value now, so we can return it
	if v, ok := a.Peek(); ok {
		return v, nil
	}
	// Evaluation reported this result complete, but this accessor is still
	// empty: it is a copy taken from the result's canonical value before
	// that value realized. The canonical value holds the content — mirror
	// its field into this copy instead of failing.
	if a.fill != nil {
		if v, ok := a.fill(res.Self()); ok {
			return a.setValueIfUnset(v), nil
		}
	}
	return zero, fmt.Errorf("lazy accessor value not set after evaluation")
}

// Peek returns the current stored value without triggering lazy evaluation.
func (a *LazyAccessor[V, T]) Peek() (V, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.isSet {
		return a.value, true
	}
	var zero V
	return zero, false
}

// should only be called by implementations of evaluate for the relevant type!
func (a *LazyAccessor[V, T]) setValue(v V) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.value = v
	a.isSet = true
}

// setValueIfUnset stores v unless a value arrived concurrently, and returns
// whichever value the accessor now holds.
func (a *LazyAccessor[V, T]) setValueIfUnset(v V) V {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.isSet {
		return a.value
	}
	a.value = v
	a.isSet = true
	return v
}

// SetValue is for constructors and lazy evaluation implementations that need to
// pre-seed or materialize an accessor explicitly.
func (a *LazyAccessor[V, T]) SetValue(v V) {
	a.setValue(v)
}
