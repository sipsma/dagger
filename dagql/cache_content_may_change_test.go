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
				frame.FollowsFromInputs = false
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
	_, _, bundle, _ := otherRunTestBundleFrame(t, func(frame *ResultCall) {
		frame.FollowsFromInputs = false
		frame.ContentMayChange = true
	})
	bctx, b, bsrv := transferTestCache(t)
	persistedListTestResult(t, bctx, b, bsrv, "other-run-dep", &transferTestValue{Text: "dep, b's run"})
	reply, err := b.MergeValues(bctx, cloudCacheID, bundle)
	require.NoError(t, err)
	require.Empty(t, reply.OtherRun)
	require.True(t, reply.Committed)
}
