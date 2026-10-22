package dagql

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/dagger/dagger/internal/buildkit/util/compression"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// installContentionProfileEnv names a directory the contention test writes
// its mutex profile to, one file per subtest.
const installContentionProfileEnv = "_DAGGER_BENCH_PROFILE_DIR"

// TestOfferInstallContention measures many small offered parts installing at
// once through the real install path (the lazy-operation decision,
// installChainPart, ImportChain, PrepareReadyPart and CommitReadyPart) on a
// store whose metadata database is opened as the engine opens it. Starts are
// spread over a second, as a check's demands arrive. It runs alone and under
// each kind of work an engine does beside its installs: disk writes, as
// execs and layer applies make; commits of large snapshots; and export
// captures of a large closure. For each it reports the install durations,
// how many installs end in a burst, where the install line says the time
// went, and which callers held the metadata database's single writer.
//
// It is a measurement, not a check: it runs only with _DAGGER_BENCH set.
func TestOfferInstallContention(t *testing.T) {
	if os.Getenv(benchTestsEnv) == "" {
		t.Skip(benchTestsEnv + " is not set")
	}
	for _, tc := range []struct {
		load string
		n    int
	}{
		{"none", 1}, {"none", 8}, {"none", 32}, {"none", 64},
		{"writes", 1}, {"writes", 64},
		{"commits", 1}, {"commits", 64},
		{"exports", 1}, {"exports", 64},
	} {
		t.Run(fmt.Sprintf("load=%s/n=%d", tc.load, tc.n), func(t *testing.T) {
			runInstallContention(t, tc.load, tc.n)
		})
	}
}

// benchTestsEnv opts benchmark-style tests in, as engine-dev's TestProfile
// does.
const benchTestsEnv = "_DAGGER_BENCH"

const (
	contentionWaves  = 3
	contentionSpread = time.Second
	// One in four receivers is offered a chain the cache already holds:
	// its install downloads nothing.
	contentionHeldEvery = 4
)

type contentionInstall struct {
	entry      uint64
	held       bool
	wave       int
	start, end time.Time
	err        error
}

func runInstallContention(t *testing.T, load string, n int) {
	// The mutex profile accumulates over the process: it is written before
	// and after the waves, for the difference (pprof -diff_base), and the
	// test is best run one subtest per process.
	profile := func(string) {}
	if dir := os.Getenv(installContentionProfileEnv); dir != "" {
		previous := runtime.SetMutexProfileFraction(1)
		t.Cleanup(func() { runtime.SetMutexProfileFraction(previous) })
		name := strings.NewReplacer("/", "_", "=", "-").Replace(t.Name())
		profile = func(suffix string) {
			f, err := os.Create(filepath.Join(dir, "mutex-"+name+suffix+".pprof"))
			require.NoError(t, err)
			defer f.Close()
			require.NoError(t, pprof.Lookup("mutex").WriteTo(f, 0))
		}
	}
	txs := &testutil.TxTimes{}
	store := testutil.NewStoreWith(t, testutil.StoreOptions{Bolt: testutil.EngineBoltOptions(), TxTimes: txs})
	ctx, c, srv := transferTestCache(t)
	c.snapshotManager = store.Manager
	transport := newContentTestTransport()
	c.partContentSource = NewPartContentSource(transport)
	logs := &offerLogs{}
	ctx = logs.into(ctx)

	chains := buildContentionChains(t, contentionWaves*n)
	address := PersistedPartAddress{Part: "snapshot"}
	route := LazyOperationRoute{Group: LazyGroupAddress{Group: LazyGroupWhole}, WriteSet: []PersistedPartAddress{address}, HasLazyOperation: true}

	stopLoad := startContentionLoad(t, ctx, c, srv, store, load)
	profile("-base")
	start := time.Now()
	var installs []contentionInstall
	for wave := range contentionWaves {
		receivers := make([]AnyResult, n)
		held := make([]bool, n)
		for i := range n {
			chain := chains[wave*n+i]
			offer := PersistedPartOffer{Address: address, Value: SnapshotValue{Kind: "directory", Path: "/"}, Chain: OfferedChain{Layers: chain.chain.Layers, Addresses: map[digest.Digest]BlobAddress{}}}
			for j, layer := range chain.chain.Layers {
				url := fmt.Sprintf("https://contention.invalid/%d/%d/%d", wave, i, j)
				transport.serve(url, chain.blobs[j])
				offer.Chain.Addresses[layer.Descriptor.Digest] = BlobAddress{URL: url}
			}
			if n > 1 && i%contentionHeldEvery == contentionHeldEvery-1 {
				ref, err := c.snapshotManager.ImportChain(ctx, &snapshots.ExportChain{Layers: chain.chain.Layers, Provider: chain.chain.Provider})
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, ref.Release(context.Background())) })
				held[i] = true
			}
			receiver := persistedListTestResult(t, ctx, c, srv, fmt.Sprintf("receiver-%d-%d", wave, i), &transferTestValue{Text: "pending"})
			partEncodedReceiver(t, ctx, c, receiver)
			id := uint64(receiver.cacheSharedResult().id)
			operation := new(renewalFallbackOperation)
			partLazyPreparationHooks.Store(id, func(context.Context) (LazyOperationInvocation, error) { return operation, nil })
			t.Cleanup(func() { partLazyPreparationHooks.Delete(id) })
			c.egraphMu.Lock()
			owner, err := c.newOfferOwnerLocked(ctx, offer.Owner)
			if err == nil {
				err = c.testAttachPartOfferLocked(receiver.cacheSharedResult(), offer.Address, &partOffer{record: offer, owner: owner})
			}
			c.egraphMu.Unlock()
			require.NoError(t, err)
			receivers[i] = receiver
		}
		results := make([]contentionInstall, n)
		var wg sync.WaitGroup
		waveStart := time.Now()
		for i, receiver := range receivers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(time.Until(waveStart.Add(contentionSpread * time.Duration(i) / time.Duration(n))))
				out := contentionInstall{entry: uint64(receiver.cacheSharedResult().id), held: held[i], wave: wave, start: time.Now()}
				demand := &PartDemandState{target: address, started: out.start}
				for {
					out.err = c.runLazyOperationDecision(ctx, receiver, address, route, demand)
					if !partCanReselect(out.err) {
						break
					}
				}
				out.end = time.Now()
				results[i] = out
			}()
		}
		wg.Wait()
		for _, out := range results {
			require.NoError(t, out.err)
		}
		installs = append(installs, results...)
	}
	profile("")
	loadReport := stopLoad()
	reportInstallContention(t, installs, logs.lines(t, "remote cache part installed"), txs.Since(start), loadReport)
}

type contentionChain struct {
	chain *snapshots.ExportChain
	blobs [][]byte
}

// buildContentionChains builds count distinct two-layer chains in one store,
// each layer a few small files, exported gzip-compressed as the engine
// exports them.
func buildContentionChains(t *testing.T, count int) []contentionChain {
	t.Helper()
	ctx := context.Background()
	source := testutil.NewStore(t)
	chains := make([]contentionChain, count)
	for k := range count {
		var parent snapshots.ImmutableRef
		for layer := range 2 {
			files := map[string][]byte{}
			for f := range 8 {
				data := make([]byte, 2048)
				_, _ = rand.Read(data)
				files[fmt.Sprintf("layer-%d/file-%d", layer, f)] = data
			}
			parent = buildContentionLayer(t, source, parent, files)
		}
		chain, err := parent.ExportChain(ctx, config.RefConfig{Compression: compression.New(compression.Gzip)})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, chain.Release(context.Background())) })
		require.Len(t, chain.Layers, 2)
		out := contentionChain{chain: chain}
		for _, layer := range chain.Layers {
			data, err := content.ReadBlob(ctx, chain.Provider, layer.Descriptor)
			require.NoError(t, err)
			out.blobs = append(out.blobs, data)
		}
		chains[k] = out
	}
	return chains
}

// buildContentionLayer commits files on parent under an owner lease the test
// releases at cleanup.
func buildContentionLayer(t *testing.T, store *testutil.Store, parent snapshots.ImmutableRef, files map[string][]byte) snapshots.ImmutableRef {
	t.Helper()
	ctx := context.Background()
	owner, err := store.Leases.Create(ctx, leases.WithRandomID())
	require.NoError(t, err)
	ctx = leases.WithLease(ctx, owner.ID)
	ref, err := commitContentionFiles(ctx, store, parent, files)
	require.NoError(t, err)
	require.NoError(t, store.Manager.AttachLease(ctx, owner.ID, ref.SnapshotID()))
	t.Cleanup(func() {
		require.NoError(t, ref.Release(context.Background()))
		require.NoError(t, store.Manager.RemoveLease(context.Background(), owner.ID))
	})
	return ref
}

func commitContentionFiles(ctx context.Context, store *testutil.Store, parent snapshots.ImmutableRef, files map[string][]byte) (snapshots.ImmutableRef, error) {
	mut, err := store.Manager.New(ctx, parent)
	if err != nil {
		return nil, err
	}
	mounted, err := mut.Mount(ctx, false)
	if err != nil {
		return nil, errors.Join(err, mut.Release(ctx))
	}
	mounter := snapshots.LocalMounter(mounted)
	root, err := mounter.Mount()
	if err != nil {
		return nil, errors.Join(err, mut.Release(ctx))
	}
	for name, data := range files {
		path := filepath.Join(root, name)
		if err = os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			err = os.WriteFile(path, data, 0o644)
		}
		if err != nil {
			return nil, errors.Join(err, mounter.Unmount(), mut.Release(ctx))
		}
	}
	if err := mounter.Unmount(); err != nil {
		return nil, errors.Join(err, mut.Release(ctx))
	}
	return mut.Commit(ctx)
}

// startContentionLoad starts the named background work and returns a stop
// function that ends it and describes what it did.
func startContentionLoad(t *testing.T, ctx context.Context, c *Cache, srv *Server, store *testutil.Store, load string) func() string {
	t.Helper()
	done := make(chan struct{})
	var wg sync.WaitGroup
	var report func() string
	switch load {
	case "none":
		report = func() string { return "" }
	case "writes":
		// As execs and layer applies write: files of every size, never
		// synced by the writer, in the store's filesystem.
		var written atomic.Int64
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dir := filepath.Join(store.Root(), "load", fmt.Sprint(g))
				for i := 0; ; i++ {
					select {
					case <-done:
						return
					default:
					}
					batch := filepath.Join(dir, fmt.Sprint(i%32))
					_ = os.RemoveAll(batch)
					big := make([]byte, 1<<20)
					_, _ = rand.Read(big)
					err := os.MkdirAll(batch, 0o755)
					if err == nil {
						err = os.WriteFile(filepath.Join(batch, "big"), big, 0o644)
					}
					for f := 0; f < 64 && err == nil; f++ {
						err = os.WriteFile(filepath.Join(batch, fmt.Sprint(f)), big[:4096], 0o644)
					}
					if !assert.NoError(t, err) {
						return
					}
					written.Add(int64(len(big)) + 64*4096)
				}
			}()
		}
		start := time.Now()
		report = func() string {
			return fmt.Sprintf("background writes: %.0f MB/s", float64(written.Load())/1e6/time.Since(start).Seconds())
		}
	case "commits":
		// As execs and imports commit snapshots: a large snapshot, many
		// files, committed over and over.
		var mu sync.Mutex
		var commits []time.Duration
		wg.Add(1)
		go func() {
			defer wg.Done()
			files := map[string][]byte{}
			for f := range 20000 {
				files[fmt.Sprintf("d%d/f%d", f%200, f)] = []byte("x")
			}
			for {
				select {
				case <-done:
					return
				default:
				}
				lease, err := store.Leases.Create(context.Background(), leases.WithRandomID())
				if !assert.NoError(t, err) {
					return
				}
				start := time.Now()
				ref, err := commitContentionFiles(leases.WithLease(context.Background(), lease.ID), store, nil, files)
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				commits = append(commits, time.Since(start))
				mu.Unlock()
				if !assert.NoError(t, errors.Join(ref.Release(context.Background()), store.Leases.Delete(context.Background(), lease))) {
					return
				}
			}
		}()
		report = func() string {
			mu.Lock()
			defer mu.Unlock()
			return fmt.Sprintf("background commits of 20000 files: %d, each written and committed in %s", len(commits), durationSummary(commits))
		}
	case "exports":
		// As the engine exports a check's results: captures of a closure
		// of 2000 entries, one after another.
		var top AnyResult
		for i := range 2000 {
			next := persistedListTestResult(t, ctx, c, srv, fmt.Sprintf("export-chain-%d", i), &transferTestValue{Text: fmt.Sprint(i)})
			if top != nil {
				transferTestDependency(c, ctx, next, top)
			}
			top = next
		}
		var mu sync.Mutex
		var walks, waits, totals []time.Duration
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				start := time.Now()
				err := c.WithExportedValues(ctx, ValueSelection{Roots: []AnyResult{top}}, config.RefConfig{}, func(_ context.Context, values *ExportedValues) error {
					mu.Lock()
					walks = append(walks, values.Phases.Walk)
					waits = append(waits, values.Phases.WalkWait)
					mu.Unlock()
					return nil
				})
				if !assert.NoError(t, err) {
					return
				}
				mu.Lock()
				totals = append(totals, time.Since(start))
				mu.Unlock()
			}
		}()
		report = func() string {
			mu.Lock()
			defer mu.Unlock()
			return fmt.Sprintf("background exports of 2000 records: %d; walk under the graph lock %s; its wait %s; whole export %s", len(totals), durationSummary(walks), durationSummary(waits), durationSummary(totals))
		}
	default:
		t.Fatalf("unknown load %q", load)
	}
	return func() string {
		close(done)
		wg.Wait()
		return report()
	}
}

func reportInstallContention(t *testing.T, installs []contentionInstall, lines []offerLine, txs []testutil.TxTime, loadReport string) {
	t.Helper()
	byEntry := map[uint64]offerLine{}
	for _, line := range lines {
		byEntry[uint64(line.int(t, "entry"))] = line
	}
	var fetched, held []time.Duration
	phases := map[string][]time.Duration{}
	for _, in := range installs {
		d := in.end.Sub(in.start)
		if in.held {
			held = append(held, d)
		} else {
			fetched = append(fetched, d)
		}
		line, ok := byEntry[in.entry]
		require.True(t, ok, "entry %d logged its install", in.entry)
		for _, key := range []string{"waited", "layer_wait", "fetch", "apply", "import", "commit"} {
			phases[key] = append(phases[key], line.duration(t, key))
		}
	}
	t.Logf("installs downloading: %s", durationSummary(fetched))
	if len(held) > 0 {
		t.Logf("installs of held chains (nothing to download): %s", durationSummary(held))
	}
	for _, key := range []string{"waited", "layer_wait", "fetch", "apply", "import", "commit"} {
		t.Logf("  %-10s %s", key, durationSummary(phases[key]))
	}
	t.Logf("installs ending in a burst (3 or more ending within %s that started over at least %s): %d of %d", burstEnds, burstStarts, inEndBursts(installs), len(installs))
	if loadReport != "" {
		t.Logf("%s", loadReport)
	}

	// The metadata database's writer: how long transactions waited for it,
	// and who held it, by total hold.
	var waits []time.Duration
	holds := map[string][]time.Duration{}
	for _, tx := range txs {
		waits = append(waits, tx.Wait)
		holds[tx.Caller] = append(holds[tx.Caller], tx.Hold)
	}
	t.Logf("metadata write transactions: %d; wait %s", len(txs), durationSummary(waits))
	callers := make([]string, 0, len(holds))
	for caller := range holds {
		callers = append(callers, caller)
	}
	total := func(ds []time.Duration) (sum time.Duration) {
		for _, d := range ds {
			sum += d
		}
		return sum
	}
	sort.Slice(callers, func(i, j int) bool { return total(holds[callers[i]]) > total(holds[callers[j]]) })
	for _, caller := range callers[:min(len(callers), 8)] {
		t.Logf("  held by %-60s total %8s; each %s", caller, total(holds[caller]).Round(time.Microsecond), durationSummary(holds[caller]))
	}
}

// A burst is three or more installs of one wave ending within burstEnds of
// each other that started over at least burstStarts: work that queued and
// was released at once. Starts are spread over contentionSpread, so
// installs that wait for nothing end about as spread as they started.
const (
	burstEnds   = 50 * time.Millisecond
	burstStarts = 3 * burstEnds
)

// inEndBursts counts the installs that end in a burst.
func inEndBursts(installs []contentionInstall) int {
	waves := map[int][]contentionInstall{}
	for _, in := range installs {
		waves[in.wave] = append(waves[in.wave], in)
	}
	count := 0
	for _, wave := range waves {
		slices.SortFunc(wave, func(a, b contentionInstall) int { return a.end.Compare(b.end) })
		for i := 0; i < len(wave); {
			j := i
			for j+1 < len(wave) && wave[j+1].end.Sub(wave[i].end) <= burstEnds {
				j++
			}
			group := wave[i : j+1]
			first, last := group[0].start, group[0].start
			for _, in := range group {
				if in.start.Before(first) {
					first = in.start
				}
				if in.start.After(last) {
					last = in.start
				}
			}
			if len(group) >= 3 && last.Sub(first) >= burstStarts {
				count += len(group)
				i = j + 1
				continue
			}
			i++
		}
	}
	return count
}

func durationSummary(ds []time.Duration) string {
	if len(ds) == 0 {
		return "none"
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	at := func(q float64) time.Duration { return sorted[int(q*float64(len(sorted)-1))].Round(10 * time.Microsecond) }
	return fmt.Sprintf("n=%d p50=%s p90=%s p99=%s max=%s", len(sorted), at(0.5), at(0.9), at(0.99), sorted[len(sorted)-1].Round(10*time.Microsecond))
}
