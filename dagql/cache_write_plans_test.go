package dagql

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Each attach, update and merged reply of the remote writes' whole script
// is planned just before it runs (PlanAttach, PlanUpdate,
// mergedReplySharesLocked), and its change then names the kinds its plan
// named, no more and no fewer: a write in shared mode would change nothing
// its plan missed, and none would take the lock to itself for nothing.
func TestAWritesPlanNamesWhatItChanges(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	check := &planCheck{t: t, sharedStateCheck: sharedStateCheck{t: t, c: cloud}}
	remoteWritesScript(t, ctx, check)
	t.Logf("writes planned: %d, of which shared: %d", check.planned, check.sharedPlans)
	require.GreaterOrEqual(t, check.planned, 15)
	require.Positive(t, check.sharedPlans)
}

// planCheck is a sharedStateCheck whose attaches, updates and merged
// replies are each planned first, the plan checked against the change.
type planCheck struct {
	sharedStateCheck
	t                    *testing.T
	planned, sharedPlans int
}

func (p *planCheck) compare(name string, plan SharedWrites, change RemoteChange) {
	p.t.Helper()
	p.planned++
	if plan != 0 {
		p.sharedPlans++
	}
	require.Equal(p.t, plan.String(), change.Shared.String(), "%s: what its plan named, against what it changed", name)
}

func (p *planCheck) AttachRemoteHolding(ctx context.Context, key HolderKey, desc RemoteHolding) (RemoteChange, error) {
	plan := p.c.PlanAttach(key, desc, PrepareRemoteHolding(key, desc))
	change, err := p.sharedStateCheck.AttachRemoteHolding(ctx, key, desc)
	p.compare(fmt.Sprintf("attach %v", key), plan, change)
	return change, err
}

func (p *planCheck) UpdateRemoteHolding(ctx context.Context, key HolderKey, u RemoteHoldingUpdate) (RemoteChange, bool, error) {
	plan := p.c.PlanUpdate(key, u, PrepareRemoteHoldingUpdate(key, u))
	change, found, err := p.sharedStateCheck.UpdateRemoteHolding(ctx, key, u)
	p.compare(fmt.Sprintf("update %v", key), plan, change)
	return change, found, err
}

func (p *planCheck) ApplyMergedReply(ctx context.Context, cache CacheID, sent ValueBundle, reply MergeReply) (RemoteChange, error) {
	var plan SharedWrites
	if apply, err := PrepareMergedReply(cache, sent, reply); err == nil {
		p.c.egraphMu.RLock()
		plan = p.c.mergedReplySharesLocked(apply)
		p.c.egraphMu.RUnlock()
	}
	change, err := p.sharedStateCheck.ApplyMergedReply(ctx, cache, sent, reply)
	p.compare(fmt.Sprintf("merged reply %v", cache), plan, change)
	return change, err
}

var _ remoteWriteOps = (*planCheck)(nil)

// The plans of the shared-writes test's own run, a kept call, a run noted
// and a content digest's taught list among them, name what each write then
// changes.
func TestPlansOfKeptCallsAndRunsNameWhatTheyChange(t *testing.T) {
	t.Parallel()
	ctx, cloud := storedPartTestCache(t, WithBlobStore())
	attach := func(key HolderKey, desc RemoteHolding) {
		t.Helper()
		plan := cloud.PlanAttach(key, desc, PrepareRemoteHolding(key, desc))
		change, err := cloud.AttachRemoteHolding(ctx, key, desc)
		require.NoError(t, err)
		require.Equal(t, plan.String(), change.Shared.String(), "attach %v", key)
	}
	first := holdingOf("plans-1")
	first.ContentDigest = testDigest("plans-content-1")
	second := holdingOf("plans-1")
	second.ContentDigest = testDigest("plans-content-2")
	attach(HolderKey{"cache-a", 1}, first)
	attach(HolderKey{"cache-b", 1}, first)
	attach(HolderKey{"cache-b", 1}, second)
	attach(HolderKey{"cache-a", 1}, first)

	test := newCallRecordTest(t, ctx, cloud)
	key := HolderKey{Cache: "cache-k", Number: uint64(test.inner.cacheSharedResult().id)}
	span := test.span(t, true, 0)
	attach(key, span)
	attach(key, span)

	_, run := runTestExport(t, ctx, cloud, "cache-r", "plans-run")
	runKey := HolderKey{Cache: run.Cache, Number: run.Number}
	for range 2 {
		update := RemoteHoldingUpdate{Parts: []PersistedPartAddress{testLiveOffer().Address}, Replacements: run.Replacements, Evaluated: true}
		plan := cloud.PlanUpdate(runKey, update, PrepareRemoteHoldingUpdate(runKey, update))
		change, _, err := cloud.UpdateRemoteHolding(ctx, runKey, update)
		require.NoError(t, err)
		require.Equal(t, plan.String(), change.Shared.String(), "the run's evaluation")
	}
}
