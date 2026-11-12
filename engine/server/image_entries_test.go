package server

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// testImageDir writes an engine image's bundle and blobs into a directory, as
// the image's build does.
type testImageDir struct{ dir string }

func (d testImageDir) WriteBundle(_ context.Context, bundle dagql.ValueBundle) error {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d.dir, dagql.BundleFile), raw, 0o644)
}

func (d testImageDir) WriteBlob(_ context.Context, dgst digest.Digest, r content.ReaderAt) error {
	name := filepath.Join(d.dir, dagql.ImageBlobPath(dgst))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	data, err := io.ReadAll(io.NewSectionReader(r, 0, r.Size()))
	if err != nil {
		return err
	}
	return os.WriteFile(name, data, 0o644)
}

// imageEntriesDir exports one evaluated file of a source engine as an engine
// image's entries, and returns their directory and bundle. The source's cache
// is saved, as an engine's is, and so has an identity.
func imageEntriesDir(t *testing.T) (string, dagql.ValueBundle) {
	t.Helper()
	src := newAdapterTestEngineAt(t, filepath.Join(t.TempDir(), "cache.db"))
	shipped := src.file(t, "shipped")
	require.NoError(t, src.cache.Evaluate(src.ctx, shipped))
	dir := t.TempDir()
	require.NoError(t, src.cache.ExportBundle(src.ctx, src.srv, []dagql.AnyResult{shipped}, exportRefConfig, testImageDir{dir}))
	raw, err := os.ReadFile(filepath.Join(dir, dagql.BundleFile))
	require.NoError(t, err)
	var bundle dagql.ValueBundle
	require.NoError(t, json.Unmarshal(raw, &bundle))
	require.Len(t, bundle.Roots, 1)
	require.NotEmpty(t, bundle.Outputs, "the file's part is an output")
	return dir, bundle
}

// Two engine images built alike number their entries alike, and every engine
// merges an image's entries from one sender. Each entry names its run by the
// cache that built it, so one image's entry is never taken for the other's.
func TestImageEntriesNameTheirBuildersRuns(t *testing.T) {
	_, first := imageEntriesDir(t)
	_, second := imageEntriesDir(t)
	root := func(b dagql.ValueBundle) dagql.TransferredValue { return b.Values[int(b.Roots[0].Ordinal)-1] }
	require.Equal(t, root(first).SenderNumber, root(second).SenderNumber, "the builds number their entries alike")
	require.NotEqual(t, dagql.TransferredRun(dagql.ImageCacheID, root(first)), dagql.TransferredRun(dagql.ImageCacheID, root(second)), "each names its builder's run")
}

// An engine merges its image's entries at start. An image without entries
// starts as before; one whose bundle names a blob the image doesn't hold
// fails the start.
func TestMergeImageEntries(t *testing.T) {
	t.Run("merges", func(t *testing.T) {
		dir, _ := imageEntriesDir(t)
		e := newAdapterTestEngine(t)
		srv := &Server{engineCache: e.cache}
		require.NoError(t, srv.mergeImageEntries(e.ctx, dir))
	})
	t.Run("no image entries", func(t *testing.T) {
		e := newAdapterTestEngine(t)
		srv := &Server{engineCache: e.cache}
		require.NoError(t, srv.mergeImageEntries(e.ctx, filepath.Join(t.TempDir(), "absent")))
		require.NoError(t, srv.mergeImageEntries(e.ctx, t.TempDir()))
	})
	t.Run("a missing blob fails the start", func(t *testing.T) {
		dir, _ := imageEntriesDir(t)
		require.NoError(t, os.RemoveAll(filepath.Join(dir, "blobs")))
		e := newAdapterTestEngine(t)
		srv := &Server{engineCache: e.cache}
		require.ErrorContains(t, srv.mergeImageEntries(e.ctx, dir), "engine image entries: blob")
	})
}

// An entry the engine image ships is never exported: the export refuses it as
// a root and leaves its part out, and exports the entries the engine made.
func TestRemoteCacheAdapterExportRefusesImageEntries(t *testing.T) {
	dir, bundle := imageEntriesDir(t)
	e := newAdapterTestEngine(t)
	require.NoError(t, e.cache.SetImageContent(dir))
	reply, err := e.cache.MergeValues(e.ctx, dagql.ImageCacheID, bundle)
	require.NoError(t, err)
	require.Len(t, reply.Roots, 1)
	shipped := reply.Roots[0].Number
	made := e.file(t, "made")
	require.NoError(t, e.cache.Evaluate(e.ctx, made))

	req := protocol.Export{
		Roots:   []uint64{shipped, entryNumber(t, made)},
		Outputs: []protocol.ExportOutput{{Number: shipped, Address: snapshotPart}, {Number: entryNumber(t, made), Address: snapshotPart}},
	}
	calls := 0
	err = e.adapter.Export(e.ctx, req, nil, func(_ context.Context, exp remotecache.Export) error {
		calls++
		require.Equal(t, []protocol.SkippedRoot{{Number: shipped, Reason: protocol.SkipRefused, Message: dagql.ErrFromImage.Error()}}, exp.Skipped)
		require.NotNil(t, exp.Bundle)
		require.Equal(t, []uint64{entryNumber(t, made)}, bundleRoots(exp.Bundle))
		require.Len(t, exp.Bundle.Outputs, 1, "only the made entry's part")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

// A cache export, such as an engine image's entries, runs on an engine busy
// with other work, whose own captures, such as the remote cache's, can hold
// a row the export needs. The export waits for a result whose closure
// reaches a row that's busy now, and exports every result once the row is
// free, as on an idle engine. A result it refuses fails the export at once,
// and a wait ends with the export's context. Each engine's cache is saved,
// as an engine's is, and so has an identity to name its runs by.
func TestExportBundleWaitsForBusyResults(t *testing.T) {
	export := func(e *adapterTestEngine, ctx context.Context, dir string, results ...dagql.AnyResult) <-chan error {
		exported := make(chan error, 1)
		go func() { exported <- e.cache.ExportBundle(ctx, e.srv, results, exportRefConfig, testImageDir{dir}) }()
		return exported
	}
	t.Run("busy", func(t *testing.T) {
		e := newAdapterTestEngineAt(t, filepath.Join(t.TempDir(), "cache.db"))
		ready := e.file(t, "ready")
		require.NoError(t, e.cache.Evaluate(e.ctx, ready))
		busy := e.busyFile(t, "busy")
		on, _ := e.publishFile(t, "on-busy", busy)
		require.NoError(t, e.cache.Evaluate(e.ctx, on))
		dir := t.TempDir()
		exported := export(e, e.ctx, dir, ready, on)
		// Give the export time to find the busy row before its body ends.
		time.Sleep(200 * time.Millisecond)
		e.bodies.unpause()
		require.NoError(t, exportErr(t, exported))
		raw, err := os.ReadFile(filepath.Join(dir, dagql.BundleFile))
		require.NoError(t, err)
		var bundle dagql.ValueBundle
		require.NoError(t, json.Unmarshal(raw, &bundle))
		require.ElementsMatch(t, []uint64{entryNumber(t, ready), entryNumber(t, on)}, bundleRoots(&bundle), "every result is exported")
	})
	t.Run("refused", func(t *testing.T) {
		e := newAdapterTestEngineAt(t, filepath.Join(t.TempDir(), "cache.db"))
		ready := e.file(t, "ready")
		require.NoError(t, e.cache.Evaluate(e.ctx, ready))
		refused := e.refusedFile(t, "refused")
		on, _ := e.publishFile(t, "on-refused", refused)
		require.NoError(t, e.cache.Evaluate(e.ctx, on))
		err := exportErr(t, export(e, e.ctx, t.TempDir(), ready, on))
		require.ErrorContains(t, err, "export bundle: 1 of 2 results left out")
		require.NotErrorIs(t, err, dagql.ErrPersistStateNotReady)
	})
	t.Run("canceled", func(t *testing.T) {
		e := newAdapterTestEngineAt(t, filepath.Join(t.TempDir(), "cache.db"))
		busy := e.busyFile(t, "busy")
		on, _ := e.publishFile(t, "on-busy", busy)
		require.NoError(t, e.cache.Evaluate(e.ctx, on))
		ctx, cancel := context.WithCancel(e.ctx)
		exported := export(e, ctx, t.TempDir(), on)
		time.Sleep(200 * time.Millisecond)
		cancel()
		require.ErrorIs(t, exportErr(t, exported), context.Canceled)
	})
}

// exportErr is the error an export sends on exported, within 10 seconds.
func exportErr(t *testing.T, exported <-chan error) error {
	t.Helper()
	select {
	case err := <-exported:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the export")
		return nil
	}
}
