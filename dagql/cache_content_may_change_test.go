package dagql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

// contentMayChangeTestObj is a result whose content can change after it is
// made, as a git mirror's does when fetching updates it.
type contentMayChangeTestObj struct {
	Name string
}

func (*contentMayChangeTestObj) Type() *ast.Type {
	return &ast.Type{NamedType: "ContentMayChangeTestObj", NonNull: true}
}

func (*contentMayChangeTestObj) CacheUsageMayChange() bool {
	return true
}

// A field whose result's type says its content may change says so on its
// call frame, so that a record carries it to another cache, or the Cloud,
// which has no schema to ask.
func TestAFieldWhoseContentMayChangeSaysSoOnItsFrame(t *testing.T) {
	t.Parallel()
	cache, err := NewCache(t.Context(), "", nil, nil)
	require.NoError(t, err)
	srv, err := NewServer(t.Context(), &persistCodecRoot{})
	require.NoError(t, err)
	srv.InstallObject(NewClass(srv, ClassOpts[*contentMayChangeTestObj]{}))
	Fields[*persistCodecRoot]{
		Func("mutable", func(context.Context, *persistCodecRoot, struct{}) (*contentMayChangeTestObj, error) {
			return &contentMayChangeTestObj{Name: "mirror"}, nil
		}),
		Func("fixed", func(context.Context, *persistCodecRoot, struct{}) (Int, error) {
			return NewInt(1), nil
		}),
	}.Install(srv)
	ctx := persistDecodeOwnerContext(t, cache, srv, "content-may-change")
	for field, want := range map[string]bool{"mutable": true, "fixed": false} {
		res, err := srv.root.Select(ctx, srv, Selector{Field: field})
		require.NoError(t, err, field)
		frame, err := res.ResultCall()
		require.NoError(t, err, field)
		require.Equal(t, want, frame.ContentMayChange, field)
		require.Equal(t, want, frame.clone().ContentMayChange, field+", cloned")
	}
}

// A step whose content may change is its engine's own state, not a run of a
// step: the Cloud stores no content of it, so a value over it, which states
// its own declaration, isn't withheld for want of its content. The Cloud
// reads it from the stored record, held whole or compressed.
func TestAValueOverAStepWhoseContentMayChangeIsNotWithheld(t *testing.T) {
	t.Parallel()
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "record held whole", true: "record compressed"}[compressed], func(t *testing.T) {
			t.Parallel()
			ctx, cloud, r, p, key, replacements := withholdTestCloudFrame(t, func(frame *ResultCall) {
				frame.Follows = ""
				frame.ContentMayChange = true
			})
			withholdTestRan(t, ctx, cloud, p, key, replacements, false)
			if compressed {
				storedTestCompress(cloud)
			}
			cloud.egraphMu.RLock()
			entry := cloud.resultsByID[sharedResultID(p)]
			mayChange, canDiffer := cloud.entryContentMayChangeLocked(entry), cloud.entryCanDifferLocked(entry)
			cloud.egraphMu.RUnlock()
			require.True(t, mayChange, "the Cloud reads it from the record")
			require.False(t, canDiffer, "it is no run that can differ")
			got, err := cloud.StoredBundle(ctx, []uint64{r}, time.Now().Unix(), nil)
			require.NoError(t, err)
			require.Empty(t, got.Withheld)
		})
	}
}

// An engine that ran a step whose content may change takes a served value
// over another engine's record of it: what it holds of that state is its
// own, keeping it refuses nothing, and the value states its own declaration.
func TestAMergeOverAStepWhoseContentMayChangeIsNotRefused(t *testing.T) {
	t.Parallel()
	_, _, bundle, p := otherRunTestBundleFrame(t, func(frame *ResultCall) {
		frame.Follows = ""
		frame.ContentMayChange = true
	})
	bctx, b, bsrv := transferTestCache(t)
	persistedListTestResult(t, bctx, b, bsrv, "other-run-dep", &transferTestValue{Text: "dep, b's run"})
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.NotContains(t, reply.OtherRun, TransferOrdinal(p), "its copy of the step is its own")
	require.Empty(t, reply.OtherRun)
	require.True(t, reply.Committed)
}

// contentMayChangeTestStep is secondRunTestStep whose frame says its
// content may change.
func contentMayChangeTestStep(t *testing.T, ctx context.Context, c *Cache, srv *Server, text string) AnyResult {
	t.Helper()
	res := secondRunTestStep(t, ctx, c, srv, "may-change-step", text)
	c.egraphMu.Lock()
	frame := res.cacheSharedResult().loadResultCall().clone()
	frame.ContentMayChange = true
	res.cacheSharedResult().storeResultCall(frame)
	c.egraphMu.Unlock()
	return res
}

// A second engine's record of a step whose content may change is no second
// run: what each engine holds of such a step is its own state, which no
// store keeps, so the Cloud folds the record into the first engine's entry
// as it does any record it holds the recipe of, with a unique result over
// it too.
func TestASecondEnginesRecordOfAStepWhoseContentMayChangeIsNoSecondRun(t *testing.T) {
	t.Parallel()
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, a, asrv := transferTestCache(t)
	aStep := contentMayChangeTestStep(t, actx, a, asrv, "a-state")
	bundle, reply := secondRunTestExport(t, actx, a, nil, cloud, cctx, "cache-a", aStep)
	first := secondRunTestNumber(bundle, reply, aStep)
	require.NotZero(t, first)
	recipe := secondRunTestRecipe(t, cloud, first)

	bctx, b, bsrv := transferTestCache(t)
	bStep := contentMayChangeTestStep(t, bctx, b, bsrv, "b-state")
	bRoot := secondRunTestStep(t, bctx, b, bsrv, "may-change-unique", "b-root")
	transferTestDependency(b, bctx, bRoot, bStep)
	_, err := cloud.AttachRemoteHolding(cctx, HolderKey{Cache: "cache-b", Number: uint64(bStep.cacheSharedResult().id)}, RemoteHolding{Recipe: recipe})
	require.NoError(t, err)
	bundle, reply = secondRunTestExport(t, bctx, b, nil, cloud, cctx, "cache-b", bRoot)
	require.NotZero(t, secondRunTestNumber(bundle, reply, bRoot))
	require.Equal(t, first, secondRunTestNumber(bundle, reply, bStep), "cache-b's record lands on the first engine's entry")
	require.False(t, secondRunTestFacts(t, cloud, first).SecondRun)

	cloud.egraphMu.RLock()
	var ofRecipe []uint64
	for _, res := range cloud.entriesOfRecipeLocked(recipe) {
		ofRecipe = append(ofRecipe, uint64(res.id))
	}
	cloud.egraphMu.RUnlock()
	require.Equal(t, []uint64{first}, ofRecipe, "the recipe has one entry")
}
