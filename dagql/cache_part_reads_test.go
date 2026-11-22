package dagql

import (
	"context"
	"sync"
	"testing"
	"time"

	digest "github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"
)

// engineWideReads are the reads of the engine-wide report among reads: those
// that carry no session.
func engineWideReads(reads []PartRead) []PartRead {
	var out []PartRead
	for _, read := range reads {
		if read.Session == "" {
			out = append(out, read)
		}
	}
	return out
}

// newPartsReadTestResult is newPartsTestResult under a field of its own, so
// that results of different fields are different entries.
func newPartsReadTestResult(t *testing.T, c *Cache, ctx context.Context, obj *cacheTestPartsObject, field string) ObjectResult[*cacheTestPartsObject] {
	t.Helper()
	srv := cacheTestPartsServer(t)
	frame := &ResultCall{
		Kind:  ResultCallKindField,
		Type:  NewResultCallType((&cacheTestPartsObject{}).Type()),
		Field: field,
	}
	resAny, err := c.GetOrInitCall(ctx, cacheTestSessionID(t, ctx), srv, &CallRequest{ResultCall: frame}, func(context.Context) (AnyResult, error) {
		return NewObjectResultForCall(obj, srv, frame)
	})
	assert.NilError(t, err)
	return resAny.(ObjectResult[*cacheTestPartsObject])
}

// newPartsReadTestValue is a parts value whose output group completes its
// filesystem part, after running before, when set.
func newPartsReadTestValue(t *testing.T, c *Cache, ctx context.Context, field string, before func(context.Context) error) ObjectResult[*cacheTestPartsObject] {
	t.Helper()
	obj := &cacheTestPartsObject{resolveFn: partsTestDirectResolve}
	var res ObjectResult[*cacheTestPartsObject]
	obj.groupEval = map[LazyGroupKey]LazyEvalFunc{partsTestGroupOut: func(ctx context.Context) error {
		if before != nil {
			if err := before(ctx); err != nil {
				return err
			}
		}
		return c.partHostFor(res.cacheSharedResult()).RunNative(ctx, partsTestGroupOut, []PartKey{partsTestPartFS}, func(context.Context) error { return nil })
	}}
	res = newPartsReadTestResult(t, c, ctx, obj, field)
	return res
}

// The cache reports the parts of entries that work reads, when a part is
// read for the first time: a consumer that evaluates one part of a
// dependency reads that part, and one it evaluates whole, the whole entry.
// Each report about an entry names every part read of it so far, so a
// reader that missed an earlier one loses nothing; a part read again isn't
// reported again, nor an entry read whole. An entry the cache no longer
// holds leaves nothing behind.
func TestCacheReportsPartsRead(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported [][]PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, engineWideReads(reads))
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })

	part := newPartsReadTestValue(t, c, ctx, "read-a-part", nil)
	whole := newPartsReadTestValue(t, c, ctx, "read-whole", nil)
	consumer := newPartsReadTestValue(t, c, ctx, "consumer", func(ctx context.Context) error {
		if err := c.EvaluateParts(ctx, part, partsTestPartFS); err != nil {
			return err
		}
		if err := c.EvaluateParts(ctx, whole, partsTestPartFS); err != nil {
			return err
		}
		return c.Evaluate(ctx, whole)
	})
	assert.NilError(t, c.EvaluateParts(ctx, consumer, partsTestPartFS))
	assert.NilError(t, c.EvaluateParts(ctx, part, partsTestPartFS))
	assert.NilError(t, c.EvaluateParts(ctx, whole, partsTestPartFS))
	c.partReads.flush()
	assert.NilError(t, c.EvaluateParts(ctx, part, partsTestPartXMeta))
	assert.NilError(t, c.EvaluateParts(ctx, part, partsTestPartFS))
	c.partReads.flush()

	id := func(r ObjectResult[*cacheTestPartsObject]) uint64 { return uint64(r.cacheSharedResult().id) }
	mu.Lock()
	assert.DeepEqual(t, [][]PartRead{
		{
			{ResultID: id(consumer), Part: partsTestPartFS},
			{ResultID: id(part), Part: partsTestPartFS},
			{ResultID: id(whole)},
		},
		{
			{ResultID: id(part), Part: partsTestPartFS},
			{ResultID: id(part), Part: partsTestPartXMeta},
		},
	}, reported)
	mu.Unlock()

	cacheTestReleaseSession(t, c, ctx)
	// A lazy evaluation's operation can still be ending when ReleaseSession
	// returns, and the release's cleanup, which forgets the entries' reads,
	// then runs when it ends.
	assert.NilError(t, c.WaitSessionRelease(ctx, cacheTestSessionID(t, ctx)))
	c.partReads.mu.Lock()
	defer c.partReads.mu.Unlock()
	assert.Equal(t, 0, len(c.partReads.seen), "released entries' reads are forgotten")
}

// A flush, as the cache's close makes, returns only once a report already
// under way, such as the timer's, has been made: no read is reported after
// the cache closed.
func TestPartReadFlushWaitsForAReportUnderWay(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var mu sync.Mutex
	var reports [][]PartRead
	log := &partReadLog{seen: map[sharedResultID]*partReadsSeen{}, pending: map[sharedResultID]struct{}{}}
	log.report = func(reads []PartRead) {
		mu.Lock()
		reports = append(reports, reads)
		first := len(reports) == 1
		mu.Unlock()
		if first {
			close(started)
			<-release
		}
	}
	log.record(1, []PartKey{"fs"})
	go log.flush()
	<-started

	closed := make(chan struct{})
	go func() {
		log.flush()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("the close's flush returned while the timer's report was under way")
	case <-time.After(200 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	<-closed
	mu.Lock()
	defer mu.Unlock()
	assert.DeepEqual(t, reports, [][]PartRead{{{ResultID: 1, Part: "fs"}}})
}

// A read is reported once work asks for the part, not once the part is made:
// a consumer waiting on a dependency still running has read it, and a service
// weighing the dependency as it completes knows so.
func TestCacheReportsAPartReadWhileItsWorkRuns(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, engineWideReads(reads)...)
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })

	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	running := newPartsReadTestValue(t, c, ctx, "running", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- c.EvaluateParts(ctx, running, partsTestPartFS) }()
	<-started

	c.partReads.flush()
	mu.Lock()
	assert.DeepEqual(t, []PartRead{{ResultID: uint64(running.cacheSharedResult().id), Part: partsTestPartFS}}, reported)
	mu.Unlock()
	once.Do(func() { close(release) })
	assert.NilError(t, <-done)
}

// A read made under an uncached call, which runs whenever the call it is made
// under does, is reported once per part and session, with the session, the
// recipe of the cached call it was made under and when it was made, beside
// the engine-wide report. A second read of a part in the session isn't
// reported again. A cached call made under the uncached call, and the
// evaluation of a result the uncached call reads, read on their own account,
// as any cached call's run does: their reads carry no session.
func TestCacheReportsPartsReadUnderAnUncachedCall(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, reads...)
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })
	session := cacheTestSessionID(t, ctx)
	srv := cacheTestPartsServer(t)

	dep := newPartsReadTestValue(t, c, ctx, "read-by-the-uncached-call", nil)
	readByEvaluation := newPartsReadTestValue(t, c, ctx, "read-by-an-evaluation", nil)
	evaluated := newPartsReadTestValue(t, c, ctx, "evaluated-for-the-uncached-call", func(ctx context.Context) error {
		return c.EvaluateParts(ctx, readByEvaluation, partsTestPartFS)
	})
	byCached := newPartsReadTestValue(t, c, ctx, "read-by-a-cached-call", nil)
	call := func(ctx context.Context, field string, uncached bool, fn func(context.Context) error) {
		t.Helper()
		frame := &ResultCall{Kind: ResultCallKindField, Type: NewResultCallType((&cacheTestPartsObject{}).Type()), Field: field}
		_, err := c.GetOrInitCall(ctx, session, srv, &CallRequest{ResultCall: frame, DoNotCache: uncached}, func(ctx context.Context) (AnyResult, error) {
			if err := fn(ctx); err != nil {
				return nil, err
			}
			return NewObjectResultForCall(&cacheTestPartsObject{resolveFn: partsTestDirectResolve}, srv, frame)
		})
		assert.NilError(t, err)
	}
	const caller = digest.Digest("xxh3:0123456789abcdef")
	before := time.Now()
	call(ContextWithCallerRecipe(ctx, caller), "uncached-start", true, func(ctx context.Context) error {
		if err := c.EvaluateParts(ctx, dep, partsTestPartXMeta); err != nil {
			return err
		}
		if err := c.EvaluateParts(ctx, dep, partsTestPartFS); err != nil {
			return err
		}
		if err := c.EvaluateParts(ctx, dep, partsTestPartFS); err != nil {
			return err
		}
		// The uncached call reads evaluated; what evaluated's own
		// evaluation reads is that result's call's.
		if err := c.EvaluateParts(ctx, evaluated, partsTestPartFS); err != nil {
			return err
		}
		call(ctx, "cached-under-the-uncached-call", false, func(ctx context.Context) error {
			return c.EvaluateParts(ctx, byCached, partsTestPartFS)
		})
		return nil
	})
	// The same session reads the part again, under another uncached call.
	call(ctx, "uncached-again", true, func(ctx context.Context) error {
		return c.EvaluateParts(ctx, dep, partsTestPartFS)
	})
	c.partReads.flush()

	id := func(r ObjectResult[*cacheTestPartsObject]) uint64 { return uint64(r.cacheSharedResult().id) }
	mu.Lock()
	defer mu.Unlock()
	var sessionReads []PartRead
	engineWide := map[uint64][]PartKey{}
	for _, read := range reported {
		if read.Session == "" {
			engineWide[read.ResultID] = append(engineWide[read.ResultID], read.Part)
			continue
		}
		assert.Assert(t, !read.At.Before(before) && !read.At.After(time.Now()), "a read's time is when it was made")
		read.At = time.Time{}
		sessionReads = append(sessionReads, read)
	}
	assert.DeepEqual(t, []PartRead{
		{ResultID: id(dep), Part: partsTestPartXMeta, Session: session, Caller: caller},
		{ResultID: id(dep), Part: partsTestPartFS, Session: session, Caller: caller},
		{ResultID: id(evaluated), Part: partsTestPartFS, Session: session, Caller: caller},
	}, sessionReads)
	assert.DeepEqual(t, []PartKey{partsTestPartFS}, engineWide[id(readByEvaluation)])
	assert.DeepEqual(t, []PartKey{partsTestPartFS}, engineWide[id(byCached)])
	assert.Assert(t, len(engineWide[id(dep)]) > 0, "the engine-wide report still names the entry")
}

// A read counts when the call it is made in is uncached or session-bound, or
// when it is made at the session's top, outside any call, and it carries the
// nearest call above it that is neither, or an empty caller at the session's
// top. So a read at the session's top carries no caller, a session-bound
// call's own read carries the caller above that call, not the session-bound
// call, whose recipe names the session, and a cached call's own read, which
// happens only when its lookup misses, doesn't count.
func TestCacheReportsPartsReadAtTheSessionTopAndUnderSessionBoundCalls(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, reads...)
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })
	session := cacheTestSessionID(t, ctx)
	srv := cacheTestPartsServer(t)

	atTop := newPartsReadTestValue(t, c, ctx, "read-at-the-session-top", nil)
	bySessionBoundAtTop := newPartsReadTestValue(t, c, ctx, "read-by-a-session-bound-call-at-the-top", nil)
	bySessionBoundUnderCall := newPartsReadTestValue(t, c, ctx, "read-by-a-session-bound-call-under-a-cached-call", nil)
	byCached := newPartsReadTestValue(t, c, ctx, "read-by-a-cached-call", nil)
	call := func(ctx context.Context, field string, sessionBound bool, fn func(context.Context) error) {
		t.Helper()
		frame := &ResultCall{Kind: ResultCallKindField, Type: NewResultCallType((&cacheTestPartsObject{}).Type()), Field: field}
		if sessionBound {
			arg, err := resultCallArgFromInput(ctx, PerSessionInput.Name, NewString(session), false)
			assert.NilError(t, err)
			frame.ImplicitInputs = []*ResultCallArg{arg}
		}
		_, err := c.GetOrInitCall(ctx, session, srv, &CallRequest{ResultCall: frame}, func(ctx context.Context) (AnyResult, error) {
			if err := fn(ctx); err != nil {
				return nil, err
			}
			return NewObjectResultForCall(&cacheTestPartsObject{resolveFn: partsTestDirectResolve}, srv, frame)
		})
		assert.NilError(t, err)
	}
	assert.NilError(t, c.EvaluateParts(ctx, atTop, partsTestPartFS))
	call(ctx, "session-bound-at-the-top", true, func(ctx context.Context) error {
		return c.EvaluateParts(ctx, bySessionBoundAtTop, partsTestPartFS)
	})
	const caller = digest.Digest("xxh3:fedcba9876543210")
	call(ContextWithCallerRecipe(ctx, caller), "session-bound-under-a-cached-call", true, func(ctx context.Context) error {
		return c.EvaluateParts(ctx, bySessionBoundUnderCall, partsTestPartFS)
	})
	call(ctx, "cached-at-the-top", false, func(ctx context.Context) error {
		return c.EvaluateParts(ctx, byCached, partsTestPartFS)
	})
	c.partReads.flush()

	id := func(r ObjectResult[*cacheTestPartsObject]) uint64 { return uint64(r.cacheSharedResult().id) }
	mu.Lock()
	defer mu.Unlock()
	var sessionReads []PartRead
	for _, read := range reported {
		if read.Session != "" {
			read.At = time.Time{}
			sessionReads = append(sessionReads, read)
		}
	}
	assert.DeepEqual(t, []PartRead{
		{ResultID: id(atTop), Part: partsTestPartFS, Session: session},
		{ResultID: id(bySessionBoundAtTop), Part: partsTestPartFS, Session: session},
		{ResultID: id(bySessionBoundUnderCall), Part: partsTestPartFS, Session: session, Caller: caller},
	}, sessionReads)
	assert.DeepEqual(t, []PartKey{partsTestPartFS}, func() []PartKey {
		var parts []PartKey
		for _, read := range engineWideReads(reported) {
			if read.ResultID == id(byCached) {
				parts = append(parts, read.Part)
			}
		}
		return parts
	}())
}

// A nested client's requests, such as a module's while it answers a function
// call, are made from the session's context, and carry from the call they
// answer both its recipe and what part reads made under it count as
// (CurrentReadScope, ContextWithReadScope). So under a reported session-bound
// call at the session's top, a read at the request's top and an uncached
// call's read count, and carry no caller, as they would in that call's own
// resolver, not the session-bound call, whose recipe names the session. Under
// a reported cached call, an uncached call's read carries that call, and a
// read at the request's top, which happens only when the call's lookup
// misses, doesn't count.
func TestCacheReportsPartsReadUnderANestedClientsRequest(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, reads...)
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })
	session := cacheTestSessionID(t, ctx)
	srv := cacheTestPartsServer(t)

	atTopUnderSessionBound := newPartsReadTestValue(t, c, ctx, "read-at-a-request's-top-under-a-session-bound-call", nil)
	uncachedUnderSessionBound := newPartsReadTestValue(t, c, ctx, "read-by-an-uncached-call-under-a-session-bound-call", nil)
	atTopUnderCached := newPartsReadTestValue(t, c, ctx, "read-at-a-request's-top-under-a-cached-call", nil)
	uncachedUnderCached := newPartsReadTestValue(t, c, ctx, "read-by-an-uncached-call-under-a-cached-call", nil)
	call := func(ctx context.Context, field string, sessionBound, uncached bool, fn func(context.Context) error) {
		t.Helper()
		frame := &ResultCall{Kind: ResultCallKindField, Type: NewResultCallType((&cacheTestPartsObject{}).Type()), Field: field}
		if sessionBound {
			arg, err := resultCallArgFromInput(ctx, PerSessionInput.Name, NewString(session), false)
			assert.NilError(t, err)
			frame.ImplicitInputs = []*ResultCallArg{arg}
		}
		_, err := c.GetOrInitCall(ctx, session, srv, &CallRequest{ResultCall: frame, DoNotCache: uncached, CacheReported: !uncached}, func(ctx context.Context) (AnyResult, error) {
			if err := fn(ctx); err != nil {
				return nil, err
			}
			return NewObjectResultForCall(&cacheTestPartsObject{resolveFn: partsTestDirectResolve}, srv, frame)
		})
		assert.NilError(t, err)
	}
	// request makes a request as a nested client does for the call answered
	// runs under: from the session's context, carrying that call's recipe
	// and read scope.
	request := func(answered context.Context, fn func(context.Context) error) error {
		return fn(ContextWithReadScope(ContextWithCallerRecipe(ctx, CallerRecipe(answered)), CurrentReadScope(answered)))
	}
	call(ctx, "session-bound-at-the-top", true, false, func(answered context.Context) error {
		return request(answered, func(ctx context.Context) error {
			if err := c.EvaluateParts(ctx, atTopUnderSessionBound, partsTestPartFS); err != nil {
				return err
			}
			call(ctx, "uncached-under-the-session-bound-call", false, true, func(ctx context.Context) error {
				return c.EvaluateParts(ctx, uncachedUnderSessionBound, partsTestPartFS)
			})
			return nil
		})
	})
	var cached digest.Digest
	call(ctx, "cached-at-the-top", false, false, func(answered context.Context) error {
		cached = CallerRecipe(answered)
		return request(answered, func(ctx context.Context) error {
			if err := c.EvaluateParts(ctx, atTopUnderCached, partsTestPartFS); err != nil {
				return err
			}
			call(ctx, "uncached-under-the-cached-call", false, true, func(ctx context.Context) error {
				return c.EvaluateParts(ctx, uncachedUnderCached, partsTestPartFS)
			})
			return nil
		})
	})
	assert.Assert(t, cached != "", "a reported cached call is the caller of the calls under it")
	c.partReads.flush()

	id := func(r ObjectResult[*cacheTestPartsObject]) uint64 { return uint64(r.cacheSharedResult().id) }
	mu.Lock()
	defer mu.Unlock()
	var sessionReads []PartRead
	for _, read := range reported {
		if read.Session != "" {
			read.At = time.Time{}
			sessionReads = append(sessionReads, read)
		}
	}
	assert.DeepEqual(t, []PartRead{
		{ResultID: id(atTopUnderSessionBound), Part: partsTestPartFS, Session: session},
		{ResultID: id(uncachedUnderSessionBound), Part: partsTestPartFS, Session: session},
		{ResultID: id(uncachedUnderCached), Part: partsTestPartFS, Session: session, Caller: cached},
	}, sessionReads)
}

// A read carries the outermost call above it that is neither uncached nor
// session-bound: the call whose run is the earliest a next engine knows the
// read will come. Under two cached calls, it is the outer one. Under a
// session-bound call at the session's top, whose recipe names the session,
// it is the cached call below it, whether that call is made in the
// session-bound call's own resolver or in a nested client's request it
// answers.
func TestCacheReportsTheOutermostCallAPartReadWaitsFor(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var reported []PartRead
	ctx := cacheTestContext(t.Context())
	c, err := NewCache(ctx, "", nil, nil, WithPartReadReport(func(reads []PartRead) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, reads...)
	}))
	assert.NilError(t, err)
	ctx = ContextWithCache(ctx, c)
	t.Cleanup(func() { assert.NilError(t, c.CloseDiscardingPersistence()) })
	session := cacheTestSessionID(t, ctx)
	srv := cacheTestPartsServer(t)

	top := ctx
	underTwo := newPartsReadTestValue(t, c, ctx, "read-under-two-cached-calls", nil)
	underSessionBound := newPartsReadTestValue(t, c, ctx, "read-under-a-cached-call-under-a-session-bound-call", nil)
	acrossRequest := newPartsReadTestValue(t, c, ctx, "read-under-a-cached-call-in-a-request-under-a-session-bound-call", nil)
	call := func(ctx context.Context, field string, sessionBound, uncached bool, fn func(context.Context) error) {
		t.Helper()
		frame := &ResultCall{Kind: ResultCallKindField, Type: NewResultCallType((&cacheTestPartsObject{}).Type()), Field: field}
		if sessionBound {
			arg, err := resultCallArgFromInput(ctx, PerSessionInput.Name, NewString(session), false)
			assert.NilError(t, err)
			frame.ImplicitInputs = []*ResultCallArg{arg}
		}
		_, err := c.GetOrInitCall(ctx, session, srv, &CallRequest{ResultCall: frame, DoNotCache: uncached, CacheReported: !uncached}, func(ctx context.Context) (AnyResult, error) {
			if err := fn(ctx); err != nil {
				return nil, err
			}
			return NewObjectResultForCall(&cacheTestPartsObject{resolveFn: partsTestDirectResolve}, srv, frame)
		})
		assert.NilError(t, err)
	}
	read := func(name string, value ObjectResult[*cacheTestPartsObject]) func(context.Context) error {
		return func(ctx context.Context) error {
			call(ctx, name, false, true, func(ctx context.Context) error {
				return c.EvaluateParts(ctx, value, partsTestPartFS)
			})
			return nil
		}
	}
	var outer, helper, helperAcross digest.Digest
	call(ctx, "outer-cached", false, false, func(ctx context.Context) error {
		outer = CallerRecipe(ctx)
		call(ctx, "inner-cached", false, false, read("uncached-under-two", underTwo))
		return nil
	})
	call(ctx, "session-bound-top", true, false, func(ctx context.Context) error {
		call(ctx, "helper", false, false, func(ctx context.Context) error {
			helper = CallerRecipe(ctx)
			return read("uncached-under-the-helper", underSessionBound)(ctx)
		})
		// A nested client's request, from the session's context, carrying
		// the session-bound call's recipe and read scope.
		nested := ContextWithReadScope(ContextWithCallerRecipe(top, CallerRecipe(ctx)), CurrentReadScope(ctx))
		call(nested, "helper-across", false, false, func(ctx context.Context) error {
			helperAcross = CallerRecipe(ctx)
			return read("uncached-under-the-helper-across", acrossRequest)(ctx)
		})
		return nil
	})
	assert.Assert(t, outer != "" && helper != "" && helperAcross != "")
	c.partReads.flush()

	id := func(r ObjectResult[*cacheTestPartsObject]) uint64 { return uint64(r.cacheSharedResult().id) }
	mu.Lock()
	defer mu.Unlock()
	var sessionReads []PartRead
	for _, read := range reported {
		if read.Session != "" {
			read.At = time.Time{}
			sessionReads = append(sessionReads, read)
		}
	}
	assert.DeepEqual(t, []PartRead{
		{ResultID: id(underTwo), Part: partsTestPartFS, Session: session, Caller: outer},
		{ResultID: id(underSessionBound), Part: partsTestPartFS, Session: session, Caller: helper},
		{ResultID: id(acrossRequest), Part: partsTestPartFS, Session: session, Caller: helperAcross},
	}, sessionReads)
}
