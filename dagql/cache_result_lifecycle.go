package dagql

import (
	"context"
	"fmt"
	"sync"
)

// This file implements the shared waiter protocol behind a result's
// materialization lifecycle. A published sharedResult moves through up to
// three coordination points before its payload is fully usable:
//
//	publish gate  — producer-driven barrier covering dependency attachment.
//	                Opened by the publisher before the result becomes visible,
//	                finished exactly once with the attachment outcome. Readers
//	                that arrive while it is open wait; the outcome is sticky.
//
//	decode stage  — demand-driven singleflight for decoding a persisted
//	                envelope into the typed payload. Completion is observable
//	                in payload state (hasValue / persistedEnvelope), not in the
//	                stage itself: each run resets at finish so a new caller can
//	                immediately attempt a fresh decode after a failure.
//
//	eval stage    — demand-driven singleflight for deferred (lazy) work.
//	                Success is sticky (markComplete). A failed run resets only
//	                once its last waiter drains, so callers that join a failure
//	                in flight share that one outcome instead of piling up
//	                duplicate attempts.
//
// All three share one implementation of the dangerous mechanics — joining,
// abandoning, last-waiter cancellation, and waking — and differ only in the
// policies above, which are expressed per run rather than as divergent
// hand-rolled protocols.
//
// Outcomes live on the run object a waiter actually waited on, never on
// shared mutable fields, so a late-waking waiter cannot observe a subsequent
// run's state.

// resultStage coordinates one materialization stage of a sharedResult.
// The zero value is ready to use.
type resultStage struct {
	mu sync.Mutex
	// complete is sticky success, used by stages whose runs finish with
	// markComplete (the eval stage). Once set, begin reports done and no
	// further runs start.
	complete bool
	// run is the current in-flight run, or — for runs that reset on drain —
	// a finished run whose waiters have not all departed yet.
	run *stageRun
}

// stageResetPolicy controls when a finished run is detached from its stage,
// allowing a new run to begin.
type stageResetPolicy int

const (
	// stageResetOnFinish detaches the run the moment it finishes. New callers
	// immediately begin fresh runs; waiters of the finished run still observe
	// its own outcome. Used by the decode stage, whose completion is tracked
	// in payload state externally.
	stageResetOnFinish stageResetPolicy = iota
	// stageResetOnDrain keeps a finished run attached until its last waiter
	// departs. Callers that arrive in that window join the finished run and
	// share its outcome. Used by the eval stage.
	stageResetOnDrain
	// stageResetNever keeps the finished run attached forever, making its
	// outcome sticky for all future waiters. Used by the publish gate.
	stageResetNever
)

// stageRun is one execution of a stage. The runner that began it must call
// finish exactly once; waiters hold the run and wait on its channel.
type stageRun struct {
	stage  *resultStage
	policy stageResetPolicy

	waitCh chan struct{}
	// cancel, when non-nil, is invoked with the abandonment cause if the last
	// waiter departs before the run finishes.
	cancel context.CancelCauseFunc

	// waiters is guarded by stage.mu.
	waiters int
	// err and finished are written once under stage.mu before waitCh closes;
	// waiters read err only after waitCh is closed.
	err      error
	finished bool
	// markComplete promotes the stage to sticky completion when the run
	// finishes successfully.
	markComplete bool
}

// stageJoin is a registered waiter on a run.
type stageJoin struct {
	run *stageRun
}

// beginProducer starts a producer-driven run (the publish gate). It must not
// be called while another run exists.
func (s *resultStage) beginProducer() *stageRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.run != nil {
		panic("resultStage: beginProducer with run already present")
	}
	run := &stageRun{
		stage:  s,
		policy: stageResetNever,
		waitCh: make(chan struct{}),
	}
	s.run = run
	return run
}

// waitIfBegun waits for the stage's run, if any, and returns its outcome. A
// stage that never began a run passes immediately. Outcomes of
// stageResetNever runs are sticky: every future caller observes the same
// error. Cancellation of ctx abandons the wait without affecting the run;
// abandoned reports that case so callers can distinguish the run's own
// failure from their wait being cut short.
func (s *resultStage) waitIfBegun(ctx context.Context) (err error, abandoned bool) {
	s.mu.Lock()
	run := s.run
	if run != nil {
		run.waiters++
	}
	s.mu.Unlock()
	if run == nil {
		return nil, false
	}
	return (&stageJoin{run: run}).wait(ctx)
}

// beginOrJoin either joins the stage's current run or makes the caller the
// runner of a new one.
//
// Exactly one of the return values is meaningful:
//   - done: the stage is sticky-complete (or prepare reported no work, which
//     promotes the stage to sticky-complete); nothing to do.
//   - join: a run exists; the caller is registered as a waiter and must call
//     join.wait.
//   - run: the caller became the runner and must call run.finish exactly
//     once. If beginnerJoins, the caller was also registered as a waiter and
//     must additionally call (&stageJoin{run}).wait.
//
// prepare, when non-nil, executes under the stage lock only on the
// became-runner path. It supplies the run's cancel func and may report
// ok=false to promote the stage to sticky completion instead of running
// (used when a result's lazy callback turns out to be absent). No run can be
// in flight while prepare executes, so it may safely read state that running
// callbacks mutate.
func (s *resultStage) beginOrJoin(
	policy stageResetPolicy,
	markComplete bool,
	beginnerJoins bool,
	prepare func() (cancel context.CancelCauseFunc, ok bool),
) (run *stageRun, join *stageJoin, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.complete {
		return nil, nil, true
	}
	if s.run != nil {
		s.run.waiters++
		return nil, &stageJoin{run: s.run}, false
	}
	var cancel context.CancelCauseFunc
	if prepare != nil {
		var ok bool
		cancel, ok = prepare()
		if !ok {
			s.complete = true
			return nil, nil, true
		}
	}
	run = &stageRun{
		stage:        s,
		policy:       policy,
		waitCh:       make(chan struct{}),
		cancel:       cancel,
		markComplete: markComplete,
	}
	if beginnerJoins {
		run.waiters = 1
	}
	s.run = run
	return run, nil, false
}

// finish records the run's outcome, applies the stage's completion/reset
// policy, and wakes waiters. It must be called exactly once per run.
func (r *stageRun) finish(err error) {
	s := r.stage
	s.mu.Lock()
	if r.finished {
		s.mu.Unlock()
		panic(fmt.Sprintf("stageRun: finish called twice (err=%v)", err))
	}
	r.err = err
	r.finished = true
	if err == nil && r.markComplete {
		s.complete = true
	}
	switch r.policy {
	case stageResetOnFinish:
		if s.run == r {
			s.run = nil
		}
	case stageResetOnDrain:
		if r.waiters == 0 && s.run == r {
			s.run = nil
		}
	case stageResetNever:
		// Sticky: the run stays attached so future waiters observe its
		// outcome.
	}
	s.mu.Unlock()
	close(r.waitCh)
}

// wait blocks until the joined run finishes or ctx is canceled.
//
// On a normal wake it returns the run's outcome with abandoned=false. On ctx
// cancellation it deregisters, invokes the run's cancel func with the cause
// if it was the last waiter, and returns the cause with abandoned=true so
// callers can distinguish "the run failed" from "I stopped waiting".
func (j *stageJoin) wait(ctx context.Context) (err error, abandoned bool) {
	r := j.run
	s := r.stage
	select {
	case <-r.waitCh:
		s.mu.Lock()
		err = r.err
		r.waiters--
		if r.policy == stageResetOnDrain && r.waiters == 0 && s.run == r {
			s.run = nil
		}
		s.mu.Unlock()
		return err, false
	case <-ctx.Done():
		cause := context.Cause(ctx)
		s.mu.Lock()
		r.waiters--
		lastWaiter := r.waiters == 0
		cancel := r.cancel
		s.mu.Unlock()
		if lastWaiter && cancel != nil {
			cancel(cause)
		}
		return cause, true
	}
}

// evalState couples the eval stage with the registered lazy-callback
// breadcrumb. The breadcrumb exists so HasPendingLazyEvaluation can report
// deferred work for results whose current wrapper happens to lack the
// callback; execution always derives the callback fresh from the value being
// evaluated.
type evalState struct {
	stage resultStage
	// registered is guarded by stage.mu.
	registered LazyEvalFunc
}

// register stores the callback breadcrumb if evaluation has neither
// completed nor already been registered.
func (e *evalState) register(fn LazyEvalFunc) {
	if fn == nil {
		return
	}
	e.stage.mu.Lock()
	if e.registered == nil && !e.stage.complete {
		e.registered = fn
	}
	e.stage.mu.Unlock()
}

// pending reports whether deferred work remains: evaluation has not
// completed and a callback is either registered or derivable from the
// current wrapper.
func (e *evalState) pending(derive func() LazyEvalFunc) bool {
	e.stage.mu.Lock()
	defer e.stage.mu.Unlock()
	if e.stage.complete {
		return false
	}
	if e.registered != nil {
		return true
	}
	return derive() != nil
}

// finishRun finishes an eval-stage run and, on success, drops the registered
// callback breadcrumb: completed evaluation has no deferred work left worth
// retaining a closure for.
func (e *evalState) finishRun(run *stageRun, err error) {
	run.finish(err)
	if err == nil {
		e.stage.mu.Lock()
		e.registered = nil
		e.stage.mu.Unlock()
	}
}
