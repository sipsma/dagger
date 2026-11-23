package dagql

import (
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/dagql/call"
)

// guardsTestFrame edits res's frame in its cache c: its claim, and its
// content digest when content is set.
func guardsTestFrame(t *testing.T, c *Cache, res AnyResult, claim FollowsClaim, content digest.Digest) {
	t.Helper()
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	frame := res.cacheSharedResult().loadResultCall().clone()
	frame.Follows = claim
	if content != "" {
		frame.ExtraDigests = append(frame.ExtraDigests, call.ExtraDigest{Digest: content, Label: call.ExtraDigestLabelContent})
	}
	res.cacheSharedResult().storeResultCall(frame)
}

// guardsTestRecipient has cache-c hold the recipe's current entry, the
// first engine's run, as its own call row of the step puts it there.
func guardsTestRecipient(t *testing.T, s *secondRunTestSetup) *HeldBy {
	t.Helper()
	key := HolderKey{Cache: "cache-c", Number: 1}
	_, err := s.cloud.AttachRemoteHolding(s.cctx, key, RemoteHolding{Recipe: s.recipe})
	require.NoError(t, err)
	held, ok := s.cloud.HolderEntry(key)
	require.True(t, ok)
	require.Equal(t, s.first, held, "cache-c holds the first run")
	return &HeldBy{Cache: "cache-c"}
}

// A value over a step whose content is addressed by hashes its own key
// holds (FollowsKey) holds the same whatever runs are below that step, so a
// recipient that holds another run of a step below it gets the value: the
// walk doesn't go below the key's step. Over a step that only follows from
// its inputs it is withheld.
func TestAValueOverAStepThatFollowsFromItsKeyIsNotWithheldForWhatIsBelowIt(t *testing.T) {
	t.Parallel()
	for _, claim := range []FollowsClaim{FollowsKey, FollowsInputs} {
		t.Run(string(claim), func(t *testing.T) {
			t.Parallel()
			s := newSecondRunTestSetup(t)
			read := secondRunTestStep(t, s.bctx, s.b, s.bsrv, "guards-read", "read by its key")
			guardsTestFrame(t, s.b, read, claim, "")
			over := secondRunTestStep(t, s.bctx, s.b, s.bsrv, "guards-over-read", "over the read")
			transferTestDependency(s.b, s.bctx, over, read)
			transferTestDependency(s.b, s.bctx, read, s.bStep)
			bundle, reply := secondRunTestExport(t, s.bctx, s.b, nil, s.cloud, s.cctx, "cache-b", s.bRoot, over)
			second, root := secondRunTestNumber(bundle, reply, s.bStep), secondRunTestNumber(bundle, reply, over)
			require.NotZero(t, root)
			require.NotEqual(t, s.first, second, "cache-b's run is an entry of its own")
			got, err := s.cloud.StoredBundle(s.cctx, []uint64{root}, time.Now().Unix(), guardsTestRecipient(t, s))
			require.NoError(t, err)
			if claim == FollowsKey {
				require.Empty(t, got.Withheld, "below a step its key addresses, no run shows")
			} else {
				require.Equal(t, []uint64{root}, got.Withheld)
				require.Equal(t, []uint64{second}, got.WithheldOn)
			}
		})
	}
}

// Two runs of a step whose content the cache knows to be the same are one
// run, as the cache treats them: a recipient that holds one gets a value
// over the other. Where their content differs, or isn't known for one of
// them, they are two runs and the value is withheld.
func TestAValueOverAnotherRunWithTheRecipientsContentIsNotWithheld(t *testing.T) {
	t.Parallel()
	for name, contents := range map[string][2]digest.Digest{
		"same content":      {digest.FromString("same"), digest.FromString("same")},
		"different content": {digest.FromString("a"), digest.FromString("b")},
		"content not known": {digest.FromString("a"), ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, root := guardsTestTwoRuns(t, contents[0], contents[1])
			cloud, cctx := s.cloud, s.cctx
			got, err := cloud.StoredBundle(cctx, []uint64{root}, time.Now().Unix(), guardsTestRecipient(t, s))
			require.NoError(t, err)
			if name == "same content" {
				require.Empty(t, got.Withheld, "one run, as the cache treats them")
			} else {
				require.Equal(t, []uint64{root}, got.Withheld)
			}
		})
	}
}

// guardsTestTwoRuns has cache-a's run of a step, of content digest a, merged
// into a new Cloud, then cache-b's own run of it, of content digest b, with
// a unique result over it, which the Cloud keeps apart as a second run. It
// returns the setup and the Cloud's number for cache-b's result.
func guardsTestTwoRuns(t *testing.T, a, b digest.Digest) (*secondRunTestSetup, uint64) {
	t.Helper()
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	actx, ac, asrv := transferTestCache(t)
	aStep := secondRunTestStep(t, actx, ac, asrv, "guards-step", "a-run")
	guardsTestFrame(t, ac, aStep, "", a)
	bundle, reply := secondRunTestExport(t, actx, ac, nil, cloud, cctx, "cache-a", aStep)
	s := &secondRunTestSetup{cctx: cctx, cloud: cloud, first: secondRunTestNumber(bundle, reply, aStep)}
	s.recipe = secondRunTestRecipe(t, cloud, s.first)
	bctx, bc, bsrv := transferTestCache(t)
	bStep := secondRunTestStep(t, bctx, bc, bsrv, "guards-step", "b-run")
	guardsTestFrame(t, bc, bStep, "", b)
	bRoot := secondRunTestStep(t, bctx, bc, bsrv, "guards-unique", "b-root")
	transferTestDependency(bc, bctx, bRoot, bStep)
	_, err := cloud.AttachRemoteHolding(cctx, HolderKey{Cache: "cache-b", Number: uint64(bStep.cacheSharedResult().id)}, RemoteHolding{Recipe: s.recipe})
	require.NoError(t, err)
	bundle, reply = secondRunTestExport(t, bctx, bc, nil, cloud, cctx, "cache-b", bRoot)
	second, root := secondRunTestNumber(bundle, reply, bStep), secondRunTestNumber(bundle, reply, bRoot)
	require.NotZero(t, root)
	require.NotEqual(t, s.first, second, "cache-b's run is an entry of its own")
	return s, root
}

// One bundle still carries one run of a step, even where the two runs'
// content is the same: an engine decides a recipe once, and lands a second
// row of it on the entry the first makes, which has no run yet to judge.
// The root over the second run is withheld from the bundle, as for runs
// whose content differs.
func TestABundleCarriesOneRunOfAStepWhateverItsContent(t *testing.T) {
	t.Parallel()
	same := digest.FromString("same")
	s, root := guardsTestTwoRuns(t, same, same)
	got, err := s.cloud.StoredBundle(s.cctx, []uint64{s.first, root}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Equal(t, []uint64{root}, got.Withheld, "the root over the second run, after the first run")
}

// An engine takes a served value over another run of a step it holds where
// the two runs' content is the same, and where the value reaches that step
// only through a step whose content its key addresses. It refuses one over
// another run whose content differs or isn't known.
func TestAMergeOverAnotherRunWithTheEnginesContentIsNotRefused(t *testing.T) {
	t.Parallel()
	same := digest.FromString("dep content")
	for name, tc := range map[string]struct {
		served, own digest.Digest
		readByKey   bool
		refused     bool
	}{
		"same content":                        {served: same, own: same},
		"different content":                   {served: same, own: digest.FromString("other"), refused: true},
		"content not known":                   {served: same, refused: true},
		"below a step its key addresses":      {readByKey: true},
		"below a step that follows its input": {refused: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			actx, a, asrv := transferTestCache(t)
			from := CacheID(a.Identity().ID)
			p := secondRunTestStep(t, actx, a, asrv, "guards-dep", "dep, a's run")
			guardsTestFrame(t, a, p, "", tc.served)
			root := persistedListTestResult(t, actx, a, asrv, "guards-root", &transferTestValue{Text: "root"})
			if name == "below a step its key addresses" || name == "below a step that follows its input" {
				claim := FollowsInputs
				if tc.readByKey {
					claim = FollowsKey
				}
				read := secondRunTestStep(t, actx, a, asrv, "guards-read", "read")
				guardsTestFrame(t, a, read, claim, "")
				transferTestDependency(a, actx, root, read)
				transferTestDependency(a, actx, read, p)
			} else {
				transferTestDependency(a, actx, root, p)
			}
			cctx, cloud := storedPartTestCache(t, WithBlobStore())
			exported := exportTestBundle(t, actx, a, root)
			reply, err := cloud.MergeValues(cctx, from, exported)
			require.NoError(t, err)
			rootNumber := secondRunTestNumber(exported, reply, root)
			require.NotZero(t, rootNumber)
			sent, err := cloud.StoredBundle(cctx, []uint64{rootNumber}, time.Now().Unix(), nil)
			require.NoError(t, err)
			require.Empty(t, sent.Withheld)

			bctx, b, bsrv := transferTestCache(t)
			own := secondRunTestStep(t, bctx, b, bsrv, "guards-dep", "dep, b's run")
			guardsTestFrame(t, b, own, "", tc.own)
			merged, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
			require.NoError(t, err)
			if tc.refused {
				require.NotEmpty(t, merged.OtherRun)
			} else {
				require.Empty(t, merged.OtherRun)
				require.True(t, merged.Committed)
			}
		})
	}
}

// A value's output names its owner's rows and its services directly, not
// through the value's dependencies: a part the Cloud borrows from a class
// mate names rows of the value's closure, wherever they are in it. So an
// engine refuses a value whose output names another run of a service it
// holds, even where the value reaches that service through its dependencies
// only below a step whose content its key addresses.
func TestAMergeRefusesAServiceAnOutputNamesBelowAStepItsKeyAddresses(t *testing.T) {
	t.Parallel()
	actx, a, asrv := transferTestCache(t)
	from := CacheID(a.Identity().ID)
	service := secondRunTestStep(t, actx, a, asrv, "guards-dep", "service, a's run")
	guardsTestFrame(t, a, service, "", digest.FromString("service content"))
	read := secondRunTestStep(t, actx, a, asrv, "guards-read", "read")
	guardsTestFrame(t, a, read, FollowsKey, "")
	root := persistedListTestResult(t, actx, a, asrv, "guards-root", &transferTestValue{Text: "root"})
	transferTestDependency(a, actx, root, read)
	transferTestDependency(a, actx, read, service)
	cctx, cloud := storedPartTestCache(t, WithBlobStore())
	exported := exportTestBundle(t, actx, a, root)
	reply, err := cloud.MergeValues(cctx, from, exported)
	require.NoError(t, err)
	rootNumber, serviceNumber := secondRunTestNumber(exported, reply, root), secondRunTestNumber(exported, reply, service)
	require.NotZero(t, rootNumber)
	require.NotZero(t, serviceNumber)
	member := storedBundleTestClassMember(t, cctx, cloud, rootNumber, "guards-member")
	stored, _, err := cloud.SetStoredPart(cctx, member, storedPartNaming(serviceNumber), 0)
	require.NoError(t, err)
	require.True(t, stored)
	sent, err := cloud.StoredBundle(cctx, []uint64{rootNumber}, time.Now().Unix(), nil)
	require.NoError(t, err)
	require.Empty(t, sent.Withheld)
	require.Len(t, sent.Bundle.Outputs, 1, "the root's output, borrowed, naming the service")
	output := sent.Bundle.Outputs[0]
	require.Len(t, output.Value.Services, 1)
	for _, value := range sent.Bundle.Values {
		if value.Ordinal == output.Ordinal {
			require.NotContains(t, value.DependencyIDs, output.Value.Services[0].ServiceResultID, "the root reaches the service only through the read")
		}
	}

	bctx, b, bsrv := transferTestCache(t)
	own := secondRunTestStep(t, bctx, b, bsrv, "guards-dep", "service, b's run")
	guardsTestFrame(t, b, own, "", digest.FromString("other content"))
	merged, err := b.MergeValues(bctx, cloudCacheID, sent.Bundle)
	require.NoError(t, err)
	require.NotEmpty(t, merged.OtherRun, "the output names b's other run of the service")
}
