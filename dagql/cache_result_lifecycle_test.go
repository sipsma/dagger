package dagql

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
)

func evalStyleBegin(s *resultStage, prepare func() (context.CancelCauseFunc, bool)) (*stageRun, *stageJoin, bool) {
	return s.beginOrJoin(stageResetOnDrain, true, true, prepare)
}

func decodeStyleBegin(s *resultStage) (*stageRun, *stageJoin, bool) {
	return s.beginOrJoin(stageResetOnFinish, false, false, nil)
}

func TestResultStageEvalSingleflightAndStickyCompletion(t *testing.T) {
	t.Parallel()
	var stage resultStage

	var runs atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once

	eg := new(errgroup.Group)
	for range 8 {
		eg.Go(func() error {
			run, join, done := evalStyleBegin(&stage, nil)
			if done {
				return nil
			}
			if join != nil {
				err, _ := join.wait(context.Background())
				return err
			}
			go func() {
				runs.Add(1)
				startOnce.Do(func() { close(started) })
				<-release
				run.finish(nil)
			}()
			err, _ := (&stageJoin{run: run}).wait(context.Background())
			return err
		})
	}

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stage run to start")
	}
	close(release)
	assert.NilError(t, eg.Wait())
	assert.Equal(t, int32(1), runs.Load())

	// Sticky completion: all future begins report done.
	_, _, done := evalStyleBegin(&stage, nil)
	assert.Assert(t, done)
}

func TestResultStageEvalFailureResetsAfterDrainAndAllowsRetry(t *testing.T) {
	t.Parallel()
	var stage resultStage

	failure := errors.New("first attempt failed")

	run, join, done := evalStyleBegin(&stage, nil)
	assert.Assert(t, run != nil)
	assert.Assert(t, join == nil)
	assert.Assert(t, !done)
	go run.finish(failure)
	err, abandoned := (&stageJoin{run: run}).wait(context.Background())
	assert.ErrorIs(t, err, failure)
	assert.Assert(t, !abandoned)

	// The failed run drained with its last waiter; the stage must accept a
	// fresh run that can succeed.
	run2, join2, done2 := evalStyleBegin(&stage, nil)
	assert.Assert(t, run2 != nil)
	assert.Assert(t, join2 == nil)
	assert.Assert(t, !done2)
	go run2.finish(nil)
	err, _ = (&stageJoin{run: run2}).wait(context.Background())
	assert.NilError(t, err)

	_, _, done = evalStyleBegin(&stage, nil)
	assert.Assert(t, done)
}

func TestResultStageEvalLateJoinerSharesFailedRunBeforeDrain(t *testing.T) {
	t.Parallel()
	var stage resultStage

	failure := errors.New("shared failure")

	run, _, _ := evalStyleBegin(&stage, nil)
	assert.Assert(t, run != nil)
	// The run finishes with a failure while its beginner-waiter has not
	// drained yet: a caller arriving now must join the finished run and
	// observe the same failure instead of starting a duplicate attempt.
	run.finish(failure)

	_, join2, done2 := evalStyleBegin(&stage, nil)
	assert.Assert(t, !done2)
	assert.Assert(t, join2 != nil)
	err, abandoned := join2.wait(context.Background())
	assert.ErrorIs(t, err, failure)
	assert.Assert(t, !abandoned)

	// Drain the original beginner-waiter too; the stage then resets.
	err, _ = (&stageJoin{run: run}).wait(context.Background())
	assert.ErrorIs(t, err, failure)

	run3, join3, done3 := evalStyleBegin(&stage, nil)
	assert.Assert(t, run3 != nil)
	assert.Assert(t, join3 == nil)
	assert.Assert(t, !done3)
	run3.finish(nil)
	err, _ = (&stageJoin{run: run3}).wait(context.Background())
	assert.NilError(t, err)
}

func TestResultStageLastWaiterAbandonCancelsRun(t *testing.T) {
	t.Parallel()
	var stage resultStage

	canceled := make(chan error, 1)
	run, _, _ := evalStyleBegin(&stage, func() (context.CancelCauseFunc, bool) {
		return func(cause error) {
			canceled <- cause
		}, true
	})
	assert.Assert(t, run != nil)

	abandonCause := errors.New("caller gave up")
	ctx, cancelCtx := context.WithCancelCause(context.Background())
	cancelCtx(abandonCause)
	err, abandoned := (&stageJoin{run: run}).wait(ctx)
	assert.ErrorIs(t, err, abandonCause)
	assert.Assert(t, abandoned)

	select {
	case cause := <-canceled:
		assert.ErrorIs(t, cause, abandonCause)
	case <-time.After(3 * time.Second):
		t.Fatal("run cancel was not invoked by last abandoning waiter")
	}

	// The runner still owns finishing; after it does, the stage resets.
	run.finish(context.Canceled)
	run2, _, done := evalStyleBegin(&stage, nil)
	assert.Assert(t, run2 != nil)
	assert.Assert(t, !done)
	run2.finish(nil)
}

func TestResultStageNonLastAbandonDoesNotCancelRun(t *testing.T) {
	t.Parallel()
	var stage resultStage

	var cancels atomic.Int32
	run, _, _ := evalStyleBegin(&stage, func() (context.CancelCauseFunc, bool) {
		return func(error) { cancels.Add(1) }, true
	})
	assert.Assert(t, run != nil)

	// Second waiter joins, then abandons while the beginner still waits.
	_, join, _ := evalStyleBegin(&stage, nil)
	assert.Assert(t, join != nil)
	ctx, cancelCtx := context.WithCancelCause(context.Background())
	cancelCtx(errors.New("impatient"))
	_, abandoned := join.wait(ctx)
	assert.Assert(t, abandoned)
	assert.Equal(t, int32(0), cancels.Load())

	go run.finish(nil)
	err, _ := (&stageJoin{run: run}).wait(context.Background())
	assert.NilError(t, err)
}

func TestResultStagePrepareCanPromoteToComplete(t *testing.T) {
	t.Parallel()
	var stage resultStage

	run, join, done := evalStyleBegin(&stage, func() (context.CancelCauseFunc, bool) {
		return nil, false
	})
	assert.Assert(t, run == nil)
	assert.Assert(t, join == nil)
	assert.Assert(t, done)

	// Promotion is sticky.
	_, _, done = evalStyleBegin(&stage, nil)
	assert.Assert(t, done)
}

func TestResultStageDecodeResetsAtFinishAndIsolatesRunErrors(t *testing.T) {
	t.Parallel()
	var stage resultStage

	failure := errors.New("decode failed")

	run1, join1, done1 := decodeStyleBegin(&stage)
	assert.Assert(t, run1 != nil)
	assert.Assert(t, join1 == nil)
	assert.Assert(t, !done1)

	// A waiter joins run1 before it finishes.
	_, waiterJoin, _ := decodeStyleBegin(&stage)
	assert.Assert(t, waiterJoin != nil)

	run1.finish(failure)

	// Reset-at-finish: a new beginner immediately becomes runner of a fresh
	// attempt even though run1's waiter has not drained yet.
	run2, join2, done2 := decodeStyleBegin(&stage)
	assert.Assert(t, run2 != nil)
	assert.Assert(t, join2 == nil)
	assert.Assert(t, !done2)
	run2.finish(nil)

	// The lingering waiter of run1 observes run1's failure, not run2's
	// success: outcomes are per-run, never leaked across runs.
	err, abandoned := waiterJoin.wait(context.Background())
	assert.ErrorIs(t, err, failure)
	assert.Assert(t, !abandoned)
}

func TestResultStageProducerGateStickyOutcome(t *testing.T) {
	t.Parallel()

	t.Run("never begun passes immediately", func(t *testing.T) {
		t.Parallel()
		var gate resultStage
		err, abandoned := gate.waitIfBegun(context.Background())
		assert.NilError(t, err)
		assert.Assert(t, !abandoned)
	})

	t.Run("waiters block until finish and outcome is sticky", func(t *testing.T) {
		t.Parallel()
		var gate resultStage
		run := gate.beginProducer()

		failure := errors.New("attach deps failed")
		waited := make(chan error, 1)
		go func() {
			err, _ := gate.waitIfBegun(context.Background())
			waited <- err
		}()

		select {
		case <-waited:
			t.Fatal("gate waiter returned before producer finished")
		case <-time.After(50 * time.Millisecond):
		}

		run.finish(failure)
		select {
		case err := <-waited:
			assert.ErrorIs(t, err, failure)
		case <-time.After(3 * time.Second):
			t.Fatal("gate waiter did not wake after finish")
		}

		// Sticky: future waiters observe the same outcome forever.
		err, abandoned := gate.waitIfBegun(context.Background())
		assert.ErrorIs(t, err, failure)
		assert.Assert(t, !abandoned)
	})

	t.Run("abandoning waiter reports abandonment", func(t *testing.T) {
		t.Parallel()
		var gate resultStage
		gate.beginProducer()

		cause := errors.New("caller canceled")
		ctx, cancelCtx := context.WithCancelCause(context.Background())
		cancelCtx(cause)
		err, abandoned := gate.waitIfBegun(ctx)
		assert.ErrorIs(t, err, cause)
		assert.Assert(t, abandoned)
	})
}

func TestResultStageFinishTwicePanics(t *testing.T) {
	t.Parallel()
	var stage resultStage
	run, _, _ := decodeStyleBegin(&stage)
	assert.Assert(t, run != nil)
	run.finish(nil)
	defer func() {
		assert.Assert(t, recover() != nil, "second finish must panic")
	}()
	run.finish(nil)
}

func TestEvalStateRegisterAndPending(t *testing.T) {
	t.Parallel()
	var eval evalState

	noWork := func() LazyEvalFunc { return nil }
	work := func() LazyEvalFunc { return func(context.Context) error { return nil } }

	// Nothing registered, nothing derivable: not pending.
	assert.Assert(t, !eval.pending(noWork))
	// Derivable work reports pending even when unregistered.
	assert.Assert(t, eval.pending(work))

	// Registration is remembered and survives wrappers that cannot derive.
	eval.register(func(context.Context) error { return nil })
	assert.Assert(t, eval.pending(noWork))

	// Successful completion clears pending permanently and drops the
	// breadcrumb; later registration attempts are ignored.
	run, _, _ := evalStyleBegin(&eval.stage, nil)
	eval.finishRun(run, nil)
	assert.Assert(t, !eval.pending(work))
	eval.register(func(context.Context) error { return nil })
	assert.Assert(t, !eval.pending(work))
}

func TestEvalStateFailedRunKeepsBreadcrumb(t *testing.T) {
	t.Parallel()
	var eval evalState

	eval.register(func(context.Context) error { return nil })
	run, _, _ := evalStyleBegin(&eval.stage, nil)
	eval.finishRun(run, errors.New("failed"))
	// Failure leaves the stage retryable and the registered callback in
	// place so HasPendingLazyEvaluation keeps reporting deferred work.
	assert.Assert(t, eval.pending(func() LazyEvalFunc { return nil }))
}
