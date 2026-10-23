package dagql

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// callRecordTestCall calls field in the cache, with the receiver and the
// module provenance given, if any, as a module function call frames them.
// init computes the value on a miss. It returns the result, whether init ran,
// and the call's frame.
func callRecordTestCall(t *testing.T, ctx context.Context, c *Cache, srv *Server, field string, receiver, module AnyResult, init func(*ResultCall) (AnyResult, error)) (AnyResult, bool, *ResultCall) {
	t.Helper()
	frame := &ResultCall{Kind: ResultCallKindField, Field: field, Type: NewResultCallType((&transferTestValue{}).Type())}
	if receiver != nil {
		frame.Receiver = &ResultCallRef{ResultID: uint64(receiver.cacheSharedResult().id)}
	}
	if module != nil {
		frame.Module = &ResultCallModule{ResultRef: &ResultCallRef{ResultID: uint64(module.cacheSharedResult().id)}, Name: "call-record-test"}
	}
	ran := false
	res, err := c.GetOrInitCall(ctx, "test-session", srv, &CallRequest{ResultCall: frame, IsPersistable: true}, func(context.Context) (AnyResult, error) {
		ran = true
		return init(frame)
	})
	require.NoError(t, err)
	return res, ran, frame
}

// callRecordTestValue is an init that computes a fresh value for the frame.
func callRecordTestValue(text string) func(*ResultCall) (AnyResult, error) {
	return func(frame *ResultCall) (AnyResult, error) {
		return NewResultForCall(&transferTestValue{Text: text}, frame)
	}
}

// callRecordTestModule computes a module result of field, with the content
// digest content: a module's implementation, whose recipe differs per client
// and whose content digest doesn't.
func callRecordTestModule(t *testing.T, ctx context.Context, c *Cache, srv *Server, field string, content digest.Digest) AnyResult {
	t.Helper()
	mod, _, _ := callRecordTestCall(t, ctx, c, srv, field, nil, nil, callRecordTestValue("module"))
	require.NoError(t, c.TeachContentDigest(ctx, mod, content))
	return mod
}

// callRecordTestRun is engine A's run: its module, the constructor wolfi with
// the module's provenance, an inner result, and the module function
// wolfi.container, which returns that inner result. A warm A hits the
// function call. It returns the inner result and the function call's frame.
func callRecordTestRun(t *testing.T, ctx context.Context, a *Cache, srv *Server, moduleField string, content digest.Digest) (AnyResult, *ResultCall) {
	t.Helper()
	mod := callRecordTestModule(t, ctx, a, srv, moduleField, content)
	wolfi, _, _ := callRecordTestCall(t, ctx, a, srv, "wolfi", nil, mod, callRecordTestValue("wolfi"))
	inner, _, _ := callRecordTestCall(t, ctx, a, srv, "withEnvVariable", nil, nil, callRecordTestValue("inner"))
	got, ran, frame := callRecordTestCall(t, ctx, a, srv, "container", wolfi, mod, func(*ResultCall) (AnyResult, error) { return inner, nil })
	require.True(t, ran)
	require.Equal(t, inner.cacheSharedResult().id, got.cacheSharedResult().id, "the function returns the inner result")
	again, ran, _ := callRecordTestCall(t, ctx, a, srv, "container", wolfi, mod, callRecordTestValue("recomputed"))
	require.False(t, ran, "a warm engine hits the function call")
	require.Equal(t, inner.cacheSharedResult().id, again.cacheSharedResult().id, "A's hit returns the inner result")
	require.NoError(t, cacheOwnershipError(a))
	return inner, frame
}

// callRecordTestEntry returns the cache's entry of the call's recipe: the
// call's own record, an entry other than the result the call returned.
func callRecordTestEntry(t *testing.T, ctx context.Context, c *Cache, frame *ResultCall, returned AnyResult) AnyResult {
	t.Helper()
	recipe, err := frame.deriveRecipeDigest(c)
	require.NoError(t, err)
	c.egraphMu.RLock()
	entry := c.currentEntryForRecipeLocked(recipe)
	c.egraphMu.RUnlock()
	require.NotNil(t, entry, "the cache records the module function call as an entry of its own")
	require.NotEqual(t, returned.cacheSharedResult().id, entry.id, "the call's entry is not the result it returned")
	return Result[Typed]{shared: entry}
}

// callRecordTestCloud exports the roots from engine A's cache to a Cloud cache
// and returns the Cloud's stored bundle of them.
func callRecordTestCloud(t *testing.T, ctx context.Context, a *Cache, roots ...AnyResult) ValueBundle {
	t.Helper()
	bundle := exportTestBundle(t, ctx, a, roots...)
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	reply, err := cloud.MergeValues(cctx, "cache-a", bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	var numbers []uint64
	for _, value := range reply.Values {
		numbers = append(numbers, value.Number)
	}
	stored, err := cloud.StoredBundle(cctx, numbers, nil)
	require.NoError(t, err)
	require.Empty(t, stored.Gone)
	require.Empty(t, stored.NoValue)
	return stored.Bundle
}

// callRecordTestSeed is A's run, exported to the Cloud as the call's record
// alone, and the Cloud's stored bundle of it.
func callRecordTestSeed(t *testing.T, moduleField string, content digest.Digest) ValueBundle {
	t.Helper()
	actx, a, asrv := transferTestCache(t)
	inner, frame := callRecordTestRun(t, actx, a, asrv, moduleField, content)
	return callRecordTestCloud(t, actx, a, callRecordTestEntry(t, actx, a, frame, inner))
}

// callRecordTestOnB merges the bundle into a fresh engine B, and runs B's
// module, constructor and function call. It returns whether B's constructor
// and its function call ran, and the function call's result.
func callRecordTestOnB(t *testing.T, bundle ValueBundle, moduleField string, content digest.Digest, before func(context.Context, *Cache, *Server)) (bool, bool, *transferTestValue) {
	t.Helper()
	bctx, b, bsrv := transferTestCache(t)
	if before != nil {
		before(bctx, b, bsrv)
	}
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.True(t, reply.Committed)
	mod := callRecordTestModule(t, bctx, b, bsrv, moduleField, content)
	wolfi, wolfiRan, _ := callRecordTestCall(t, bctx, b, bsrv, "wolfi", nil, mod, callRecordTestValue("wolfi on B"))
	got, containerRan, _ := callRecordTestCall(t, bctx, b, bsrv, "container", wolfi, mod, callRecordTestValue("container on B"))
	value, ok := got.Unwrap().(*transferTestValue)
	require.True(t, ok)
	return wolfiRan, containerRan, value
}

// A module function call that returns another call's result, as
// wolfi.container returns the container its body built, is a record of its
// own: its frame is the call's, and its value is the result it returned. A
// fresh engine B that merges the Cloud's bundle of the record alone hits A's
// constructor and A's function call, as a warm engine does, and gets A's
// inner result.
//   - exact: B's module has A's recipe, so B's calls have A's recipes.
//   - per client: B's module has a recipe of its own, as each client's
//     module provenance does, and A's content digest: B hits both calls by
//     the terms it derives from the records' frames.
func TestCallRecordHitsTheFunctionCallOnB(t *testing.T) {
	t.Parallel()
	content := digest.FromString("call-record-test-module-content")
	for name, bModuleField := range map[string]string{"exact": "module-a", "per client": "module-b"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bundle := callRecordTestSeed(t, "module-a", content)
			wolfiRan, containerRan, value := callRecordTestOnB(t, bundle, bModuleField, content, nil)
			require.False(t, wolfiRan, "B hits A's constructor")
			require.False(t, containerRan, "B hits A's module function call")
			require.Equal(t, "inner", value.Text, "the call returns A's inner result")
		})
	}
}

// A record whose inputs B's cache doesn't share gives B no false hit: B's
// module shares nothing with A's, so B computes its constructor, and its
// function call misses, as an ordinary miss, and runs.
func TestCallRecordGivesNoFalseHit(t *testing.T) {
	t.Parallel()
	bundle := callRecordTestSeed(t, "module-a", digest.FromString("call-record-test-module-content"))
	wolfiRan, containerRan, value := callRecordTestOnB(t, bundle, "module-b", digest.FromString("call-record-test-other-content"), nil)
	require.True(t, wolfiRan, "B's constructor of another module computes")
	require.True(t, containerRan, "B's function call misses, and runs")
	require.Equal(t, "container on B", value.Text)
}

// An engine that computed the inner result itself before the merge keeps its
// own value, and the call's record lands on it: B hits the function call and
// gets its own inner result.
func TestCallRecordReachesAnEngineThatHoldsTheValue(t *testing.T) {
	t.Parallel()
	bundle := callRecordTestSeed(t, "module-a", digest.FromString("call-record-test-module-content"))
	wolfiRan, containerRan, value := callRecordTestOnB(t, bundle, "module-b", digest.FromString("call-record-test-module-content"), func(ctx context.Context, b *Cache, srv *Server) {
		_, ran, _ := callRecordTestCall(t, ctx, b, srv, "withEnvVariable", nil, nil, callRecordTestValue("inner"))
		require.True(t, ran, "B computes the inner result before the merge")
	})
	require.False(t, wolfiRan, "B hits A's constructor")
	require.False(t, containerRan, "B hits A's module function call")
	require.Equal(t, "inner", value.Text)
}

// The call's record in the Cloud's bundle is an ordinary record: its frame is
// the call's, with the call's receiver and module as its dependencies, and
// its value names the row of the result the call returned. Nothing beside
// the records names the call.
func TestCallRecordIsAnOrdinaryRecord(t *testing.T) {
	t.Parallel()
	bundle := callRecordTestSeed(t, "module-a", digest.FromString("call-record-test-module-content"))
	byOrdinal := map[TransferOrdinal]TransferredValue{}
	for _, value := range bundle.Values {
		byOrdinal[value.Ordinal] = value
	}
	var root TransferredValue
	for _, value := range bundle.Values {
		if value.Record.Call != nil && value.Record.Call.Field == "container" {
			root = value
		}
	}
	require.NotNil(t, root.Record.Call, "the bundle has a record whose frame is the call's")
	require.NotNil(t, root.Record.Call.Module, "with the call's module provenance")

	raw, err := json.Marshal(root.Record.Envelope)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Equal(t, "returned_ref", envelope["kind"], "the record's value is the result the call returned")
	returned, ok := envelope["returnedID"].(float64)
	require.True(t, ok, "the value names the returned row")
	inner, ok := byOrdinal[TransferOrdinal(returned)]
	require.True(t, ok, "the returned row is in the bundle")
	require.Equal(t, "withEnvVariable", inner.Record.Call.Field)
	require.Contains(t, root.DependencyIDs, uint64(returned), "the record depends on the returned row")

	fields := map[string]bool{}
	for _, value := range bundle.Values {
		fields[value.Record.Call.Field] = true
	}
	require.Equal(t, map[string]bool{"container": true, "withEnvVariable": true, "wolfi": true, "module-a": true}, fields, "the record's closure: the returned row, the receiver and the module")
}

// The call's record persists like any entry: after A restarts, it is still
// A's entry of the call's recipe, A still hits the call, and a fresh engine
// that merges its export hits the call.
func TestCallRecordSurvivesARestart(t *testing.T) {
	t.Parallel()
	content := digest.FromString("call-record-test-module-content")
	path := filepath.Join(t.TempDir(), "a.db")
	actx, a, asrv := persistedListTestCache(t, path)
	asrv.InstallObject(NewClass(asrv, ClassOpts[*transferTestValue]{}))
	inner, frame := callRecordTestRun(t, actx, a, asrv, "module-a", content)
	innerID := inner.cacheSharedResult().id
	require.NoError(t, a.Close(actx))

	actx, a, asrv = persistedListTestCache(t, path)
	asrv.InstallObject(NewClass(asrv, ClassOpts[*transferTestValue]{}))
	require.Empty(t, a.PersistenceResetReason())
	entry := callRecordTestEntry(t, actx, a, frame, Result[Typed]{shared: &sharedResult{id: innerID}})
	mod := callRecordTestModule(t, actx, a, asrv, "module-a", content)
	wolfi, _, _ := callRecordTestCall(t, actx, a, asrv, "wolfi", nil, mod, callRecordTestValue("wolfi again"))
	got, ran, _ := callRecordTestCall(t, actx, a, asrv, "container", wolfi, mod, callRecordTestValue("container again"))
	require.False(t, ran, "the restarted A hits the function call")
	require.Equal(t, innerID, got.cacheSharedResult().id, "and gets its inner result")

	bundle := callRecordTestCloud(t, actx, a, entry)
	wolfiRan, containerRan, value := callRecordTestOnB(t, bundle, "module-b", content, nil)
	require.False(t, wolfiRan, "B hits A's constructor")
	require.False(t, containerRan, "B hits A's module function call, from A's restarted cache")
	require.Equal(t, "inner", value.Text)
}

// A module function that returns its receiver records a call whose
// dependency on the receiver is one ownership unit, as every dependency is:
// the call's entry owns each entry it names once, however many times.
func TestCallRecordReturningItsReceiverOwnsItOnce(t *testing.T) {
	t.Parallel()
	ctx, a, srv := transferTestCache(t)
	mod := callRecordTestModule(t, ctx, a, srv, "module-a", digest.FromString("call-record-test-module-content"))
	wolfi, _, _ := callRecordTestCall(t, ctx, a, srv, "wolfi", nil, mod, callRecordTestValue("wolfi"))
	got, ran, frame := callRecordTestCall(t, ctx, a, srv, "withPackage", wolfi, mod, func(*ResultCall) (AnyResult, error) { return wolfi, nil })
	require.True(t, ran)
	require.Equal(t, wolfi.cacheSharedResult().id, got.cacheSharedResult().id, "the function returns its receiver")
	callRecordTestEntry(t, ctx, a, frame, wolfi)
	require.NoError(t, cacheOwnershipError(a))
}

// After the result a call returned expires, with the call's entry or before
// it, the call runs again and its body computes a new result: the call's
// entry, its recipe's current entry, takes the new result as its value, as
// any entry takes its recipe's new value, and a fresh engine that merges it
// gets the new result. The entry is expired once its value is, as a lookup
// that selects it finds.
func TestCallRecordFollowsARecomputedResult(t *testing.T) {
	t.Parallel()
	for name, expireEntry := range map[string]bool{"both expired": true, "the result expired first": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			content := digest.FromString("call-record-test-module-content")
			ctx, a, srv := transferTestCache(t)
			inner, frame := callRecordTestRun(t, ctx, a, srv, "module-a", content)
			entry := callRecordTestEntry(t, ctx, a, frame, inner)
			if expireEntry {
				currentEntryTestExpire(a, entry)
			}
			currentEntryTestExpire(a, inner)

			mod := callRecordTestModule(t, ctx, a, srv, "module-a", content)
			wolfi, _, _ := callRecordTestCall(t, ctx, a, srv, "wolfi", nil, mod, callRecordTestValue("wolfi again"))
			var recomputed AnyResult
			got, ran, _ := callRecordTestCall(t, ctx, a, srv, "container", wolfi, mod, func(*ResultCall) (AnyResult, error) {
				res, ran, _ := callRecordTestCall(t, ctx, a, srv, "withEnvVariable", nil, nil, callRecordTestValue("inner again"))
				require.True(t, ran, "the expired inner result is computed again")
				recomputed = res
				return res, nil
			})
			require.True(t, ran, "the expired call runs again")
			require.Equal(t, recomputed.cacheSharedResult().id, got.cacheSharedResult().id)
			require.NotEqual(t, inner.cacheSharedResult().id, recomputed.cacheSharedResult().id)

			current := callRecordTestEntry(t, ctx, a, frame, recomputed)
			a.egraphMu.RLock()
			returnedID := current.cacheSharedResult().returnedID
			expired := a.resultExpiredAtLocked(current.cacheSharedResult(), time.Now().Unix())
			a.egraphMu.RUnlock()
			require.Equal(t, recomputed.cacheSharedResult().id, returnedID, "the call's entry names the recomputed result")
			require.False(t, expired, "the call's entry is current again")
			require.NoError(t, cacheOwnershipError(a))

			bundle := callRecordTestCloud(t, ctx, a, current)
			wolfiRan, containerRan, value := callRecordTestOnB(t, bundle, "module-b", content, nil)
			require.False(t, wolfiRan, "B hits A's constructor")
			require.False(t, containerRan, "B hits A's module function call")
			require.Equal(t, "inner again", value.Text, "and gets the recomputed result")
		})
	}
}

// A load of a call's entry by its number serves the result the call
// returned, as a lookup that selects the entry does: exactly, and for a
// session.
func TestCallRecordLoadsAsTheReturnedResult(t *testing.T) {
	t.Parallel()
	ctx, a, srv := transferTestCache(t)
	inner, frame := callRecordTestRun(t, ctx, a, srv, "module-a", digest.FromString("call-record-test-module-content"))
	entry := callRecordTestEntry(t, ctx, a, frame, inner)
	for name, session := range map[string]string{"exact": "", "for a session": "test-session"} {
		loaded, err := a.LoadResultByResultID(ctx, session, srv, uint64(entry.cacheSharedResult().id))
		require.NoError(t, err, name)
		require.Equal(t, inner.cacheSharedResult().id, loaded.cacheSharedResult().id, name)
		value, ok := loaded.Unwrap().(*transferTestValue)
		require.True(t, ok, name)
		require.Equal(t, "inner", value.Text, name)
	}
	require.NoError(t, cacheOwnershipError(a))
}

// Every waiter of a module function call's publication names the call's
// entry on its span. Telemetry records one span for a call in a session, and
// the waiter it belongs to can leave before the others do.
func TestCallRecordNamedByAWaiterThatLeavesFirst(t *testing.T) {
	t.Parallel()
	ctx, a, srv := transferTestCache(t)
	mod := callRecordTestModule(t, ctx, a, srv, "module-a", digest.FromString("call-record-test-module-content"))
	inner, _, _ := callRecordTestCall(t, ctx, a, srv, "withEnvVariable", nil, nil, callRecordTestValue("inner"))
	frame := &ResultCall{
		Kind:   ResultCallKindField,
		Field:  "container",
		Type:   NewResultCallType((&transferTestValue{}).Type()),
		Module: &ResultCallModule{ResultRef: &ResultCallRef{ResultID: uint64(mod.cacheSharedResult().id)}, Name: "call-record-test"},
	}
	recipe, err := frame.deriveRecipeDigest(a)
	require.NoError(t, err)

	release, started, hold := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var leaving atomic.Int32
	a.testBeforeWaiterLeave = func() {
		if leaving.Add(1) == 1 {
			<-hold
		}
	}
	type done struct {
		req *CallRequest
		res AnyResult
		err error
	}
	finished := make(chan done, 2)
	call := func(fn func(context.Context) (AnyResult, error)) {
		req := &CallRequest{ResultCall: frame, IsPersistable: true, ConcurrencyKey: "call-record-joined", CacheEvidence: NewCacheDecision()}
		go func() {
			res, err := a.GetOrInitCall(ctx, "test-session", srv, req, fn)
			finished <- done{req: req, res: res, err: err}
		}()
	}
	call(func(context.Context) (AnyResult, error) {
		close(started)
		<-release
		return inner, nil
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the call to start")
	}
	call(func(context.Context) (AnyResult, error) {
		t.Error("the joined call must not run")
		return inner, nil
	})
	keys := callConcurrencyKeys{callKey: recipe.String(), concurrencyKey: "call-record-joined"}
	require.Eventually(t, func() bool {
		a.callsMu.Lock()
		defer a.callsMu.Unlock()
		oc := a.ongoingCalls[keys]
		return oc != nil && oc.waiters >= 2
	}, 5*time.Second, time.Millisecond, "the second call joins the first")
	close(release)

	// One waiter is held before it leaves; the other leaves first.
	first := <-finished
	require.NoError(t, first.err)
	require.Equal(t, inner.cacheSharedResult().id, first.res.cacheSharedResult().id, "the call returns the inner result")
	number, ok := first.req.CacheEvidence.ResultNumber(first.res)
	require.True(t, ok)
	require.NotEqual(t, uint64(inner.cacheSharedResult().id), number, "the first waiter's span names the call's entry")
	state, ok := first.req.CacheEvidence.ResultState(ctx, first.res)
	require.True(t, ok)
	require.Equal(t, recipe, state.Recipe)
	require.True(t, state.Retained)
	require.Contains(t, state.Deps, uint64(inner.cacheSharedResult().id))

	close(hold)
	last := <-finished
	require.NoError(t, last.err)
	lastNumber, ok := last.req.CacheEvidence.ResultNumber(last.res)
	require.True(t, ok)
	require.Equal(t, number, lastNumber, "both waiters name the one entry")
	require.NoError(t, cacheOwnershipError(a))
}
