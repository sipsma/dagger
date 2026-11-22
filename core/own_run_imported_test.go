package core

import (
	"context"
	"testing"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/dagger/dagger/engine/snapshots/testutil"
	"github.com/stretchr/testify/require"
)

// A record another engine sent with no download address, whose recipe this
// engine then ran itself, holds this engine's run: a stored run of a value
// over it, from a third engine, is kept apart, as over a value the engine
// computed. On 19 to 21 Nov engines took stored values over a step whose
// content they couldn't fetch, ran the step themselves, and could then take
// more of the other run over their own.
func TestImportedRecordRunHereIsTheEnginesOwnRun(t *testing.T) {
	aStore, bStore, cStore := testutil.NewStore(t), testutil.NewStore(t), testutil.NewStore(t)
	actx, a, asrv := transferCache(t, aStore, "", "a")
	bctx, b, bsrv := transferCache(t, bStore, "", "b")
	cctx, c, csrv := transferCache(t, cStore, "", "c")
	platform := Platform{OS: "linux", Architecture: "amd64"}
	lazyFile := func(name, contents string) *File {
		file := &File{File: new(LazyAccessor[string, *File]), Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File]), Platform: platform, Lazy: &FileBlobLazy{LazyState: NewLazyState(), Filename: name, Contents: []byte(contents), Permissions: 0o644}}
		file.SetPath("/pending")
		return file
	}

	// A's run of the CA reaches B as a record alone.
	caA := attachTransferObject(t, actx, a, asrv, "a", "ownImportedCA", lazyFile("ca.pem", "a's ca"))
	require.NoError(t, a.Evaluate(actx, caA))
	var taken dagql.ObjectResult[*File]
	require.NoError(t, a.WithExportedValues(actx, dagql.ValueSelection{Roots: []dagql.AnyResult{caA}}, config.RefConfig{}, func(_ context.Context, exported *dagql.ExportedValues) error {
		reply, err := b.MergeValues(bctx, dagql.CloudCacheID, exported.Bundle)
		require.NoError(t, err)
		require.Len(t, reply.Imported(), 1)
		loaded, err := b.LoadResultByResultID(bctx, "", bsrv, reply.Imported()[0].ResultID)
		require.NoError(t, err)
		taken = loaded.(dagql.ObjectResult[*File])
		return nil
	}))
	// B has no address for its part, so it runs the CA's recipe itself.
	require.NoError(t, b.Evaluate(bctx, taken))

	// C's run of the CA, and a certificate made over it.
	caC := attachTransferObject(t, cctx, c, csrv, "c", "ownImportedCA", lazyFile("ca.pem", "c's ca"))
	certC := attachTransferObject(t, cctx, c, csrv, "c", "ownImportedCert", lazyFile("cert.pem", "c's cert"))
	require.NoError(t, c.AddExplicitDependency(cctx, certC, caC, "test"))
	require.NoError(t, c.Evaluate(cctx, certC))
	require.NoError(t, c.Evaluate(cctx, caC))
	require.NoError(t, c.WithExportedValues(cctx, dagql.ValueSelection{Roots: []dagql.AnyResult{certC}}, config.RefConfig{}, func(_ context.Context, exported *dagql.ExportedValues) error {
		reply, err := b.MergeValues(bctx, dagql.CloudCacheID, exported.Bundle)
		require.NoError(t, err)
		require.Equal(t, 1, reply.Rows.ApartFull, "the CA, kept apart from B's run of it")
		require.Equal(t, 1, reply.Rows.OverApartFull, "the certificate over C's")
		require.Empty(t, reply.Imported(), "B takes no certificate made over another run of its CA")
		return nil
	}))
	require.Equal(t, "a's ca", string(demandedFileContents(t, bctx, taken)), "B keeps its own run")
}
