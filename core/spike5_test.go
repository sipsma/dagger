package core

import (
	"path/filepath"
	"testing"

	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/stretchr/testify/require"
)

func TestSpike5Paths(t *testing.T) {
	for _, paths := range [][]string{
		{"/.spike5/view/a", "/.spike5/view/b"},
		{"/.spike5/view/nested/a", "/.spike5/view/nested/b"},
	} {
		if err := spike5ValidatePaths(paths); err != nil {
			t.Fatal(err)
		}
	}
	for _, paths := range [][]string{
		{"/etc/passwd"}, {"relative"}, {"/.spike5/view/../escape"},
		{"/.spike5/view/a", "/.spike5/view/a"},
		{"/.spike5/view/a", "/.spike5/view/a/b"},
		{"/.spike5/view/a/b", "/.spike5/view/a"},
	} {
		if err := spike5ValidatePaths(paths); err == nil {
			t.Fatalf("accepted %v", paths)
		}
	}
}

// Exercise the actual view builder against the ordinary persistence/transfer
// codecs. This checks metadata and snapshot-part coverage, not network I/O.
func TestSpike5TransferCodecs(t *testing.T) {
	t.Setenv("_DAGGER_SPIKE5", "1")
	ctx, cache, srv := containerPersistenceTestCache(t, filepath.Join(t.TempDir(), "cache.db"), newContainerPersistenceTestSnapshots(), "spike5")
	ctx = dagql.ContextWithServer(ctx, srv)
	platform := Platform{OS: "linux", Architecture: "amd64"}
	parent := NewContainer(platform)
	parent.FS.setValue(containerPersistenceTestDirectory("rootfs", "/"))
	parentResult := attachTransferObject(t, ctx, cache, srv, "spike5", "parent", parent)
	file := &File{Platform: platform, File: new(LazyAccessor[string, *File]), Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File])}
	file.SetPath("/result")
	file.SetSnapshot(&cacheVolumeTestImmutableRef{id: "archive", snapshotID: "archive"})
	fileResult := attachTransferObject(t, ctx, cache, srv, "spike5", "sealedShape", file)
	view, err := Spike5FileView(ctx, parentResult, []dagql.ObjectResult[*File]{fileResult}, []string{"/.spike5/view/a"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, view.OnRelease(ctx)) })
	encodedView, err := view.EncodePersistedObject(ctx, nil)
	require.NoError(t, err)
	encodedFile, err := file.EncodePersistedObject(ctx, nil)
	require.NoError(t, err)
	for _, tc := range []struct {
		family  string
		encoded dagql.PersistedObjectEncoding
		part    dagql.PartKey
	}{
		{"Container", encodedView, "mount:/.spike5/view/a"},
		{"File", encodedFile, "snapshot"},
	} {
		t.Run(tc.family, func(t *testing.T) {
			codec := foreignFamilyCodec(tc.family)
			visit := dagql.PersistedPayloadVisit{Payload: tc.encoded.JSON, SnapshotLinks: tc.encoded.SnapshotLinks}
			parts, err := codec.MapSnapshotParts(visit)
			require.NoError(t, err)
			found := false
			for _, part := range parts {
				if part.Address.Part == tc.part {
					found = true
					require.Equal(t, "completed", part.State)
					require.Equal(t, "archive", part.SnapshotID)
					require.Equal(t, "/result", part.Value.Path)
				}
			}
			require.True(t, found, "transfer must account for the archive's bytes")
			foreign, err := codec.NormalizeForeign(visit)
			require.NoError(t, err)
			visit.Payload, visit.SnapshotLinks = foreign.JSON, nil
			require.NoError(t, codec.ValidateForeign(visit))
			parts, err = codec.MapSnapshotParts(visit)
			require.NoError(t, err)
			for _, part := range parts {
				require.Empty(t, part.SnapshotID, "no producer-local snapshot ID may survive in foreign metadata")
			}
		})
	}
}
