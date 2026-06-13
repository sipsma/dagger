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
	// Completed reports whether this lazy operation already ran to success.
	// A completed Lazy is retained on its owner as the operation's recipe
	// rather than cleared, so persistence can serialize how the value was
	// produced; owners must consult lazyPending, not Lazy != nil, to decide
	// whether deferred work remains.
	Completed() bool
}

// lazyPending reports whether l represents deferred work that has not yet
// run to success. This — not a nil check — is the "does this object still
// have deferred work?" signal, since completed lazies are retained as
// recipes.
func lazyPending[T dagql.Typed](l Lazy[T]) bool {
	return l != nil && !l.Completed()
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

// Completed reports whether the lazy operation ran to success. Implementers
// embedding LazyState satisfy Lazy's Completed requirement through this
// method.
func (lazy *LazyState) Completed() bool {
	if lazy.LazyMu == nil {
		// A zero-value LazyState has no mutex and Evaluate can never have
		// run (it requires LazyMu), so the unlocked read is safe.
		return lazy.LazyInitComplete
	}
	lazy.LazyMu.Lock()
	defer lazy.LazyMu.Unlock()
	return lazy.LazyInitComplete
}

func (lazy *LazyState) Evaluate(ctx context.Context, typeName string, run func(context.Context) error) (rerr error) {
	if lazy.LazyMu == nil {
		// Completion state is read and written under LazyMu everywhere a
		// mutex exists; tolerate its absence only for the degenerate cases
		// that cannot involve concurrent evaluation.
		if lazy.LazyInitComplete {
			return nil
		}
		if run == nil {
			lazy.LazyInitComplete = true
			return nil
		}
		return fmt.Errorf("invalid %s: missing LazyMu", typeName)
	}

	lazy.LazyMu.Lock()
	defer lazy.LazyMu.Unlock()

	if lazy.LazyInitComplete {
		return nil
	}
	if run == nil {
		lazy.LazyInitComplete = true
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
}

// WARN: res MUST be the dagql result wrapper for the same owner object as this
// accessor. The accessor cannot validate that today due to the current
// Directory/File/Container vs dagql.Result split, so callers must pass the
// matching result explicitly and carefully.
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.isSet {
		return zero, fmt.Errorf("lazy accessor value not set after evaluation")
	}
	return a.value, nil
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

// SetValue is for constructors and lazy evaluation implementations that need to
// pre-seed or materialize an accessor explicitly.
func (a *LazyAccessor[V, T]) SetValue(v V) {
	a.setValue(v)
}
