package core

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
)

func TestSpike9ResultStore(t *testing.T) {
	store := testutil.NewStore(t)
	path := filepath.Join(t.TempDir(), "cache.db")
	ctx, cache, srv := transferCache(t, store, path, "spike9")
	source := func(field, contents string) dagql.ObjectResult[*File] {
		snap, _ := store.Build(t, nil, "entry", contents)
		f := &File{Platform: Platform{OS: "linux", Architecture: "amd64"}, File: new(LazyAccessor[string, *File]), Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File])}
		f.SetPath("/entry")
		f.SetSnapshot(snap)
		return attachTransferObject(t, ctx, cache, srv, "spike9", field, f)
	}
	key := Spike9Key{Provider: "test/v1", Toolchain: "toolchain/v1", Key: "action"}
	_, hit, err := Spike9Get(ctx, key)
	require.NoError(t, err)
	require.False(t, hit)
	a, b := source("a", "a"), source("b", "b")
	// Multiple publishers race on one absent key. Exactly one payload wins;
	// same-value publications converge, conflicting ones fail without replacing it.
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := map[string]int{}
	conflicts := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			src, name := a, "a"
			if i%2 != 0 {
				src, name = b, "b"
			}
			_, err := Spike9Put(ctx, key, src)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes[name]++
			} else {
				require.ErrorContains(t, err, "conflicting publication")
				conflicts++
			}
		}(i)
	}
	wg.Wait()
	require.Len(t, successes, 1)
	require.Equal(t, 8, conflicts)
	value, hit, err := Spike9Get(ctx, key)
	require.NoError(t, err)
	require.True(t, hit)
	bytes := string(demandedFileContents(t, ctx, value))
	require.Equal(t, 8, successes[bytes])
	frame, err := value.ResultCall()
	require.NoError(t, err)
	require.NotEmpty(t, frame.ContentDigest())
	for _, other := range []Spike9Key{{"test/v2", key.Toolchain, key.Key}, {key.Provider, "toolchain/v2", key.Key}, {key.Provider, key.Toolchain, "other"}} {
		_, hit, err := Spike9Get(ctx, other)
		require.NoError(t, err)
		require.False(t, hit)
	}
	// Persisted result roots, not a process map, keep entries across sessions and
	// restart. Snapshot owners are restored by the normal result-cache path.
	require.NoError(t, cache.ReleaseSession(ctx, "spike9"))
	require.NoError(t, cache.Close(ctx))
	store.Reload(t)
	ctx, _, _ = transferCache(t, store, path, "after")
	value, hit, err = Spike9Get(ctx, key)
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, bytes, string(demandedFileContents(t, ctx, value)))
}
