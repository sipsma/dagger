package snapshots

import (
	"os"
	"path/filepath"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"
)

func TestCacheBundleCreateAndOpenEmptyBundle(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "bundle-a")
	writer, err := CreateCacheBundle(dir)
	assert.NilError(t, err)
	assert.Assert(t, writer.ID != "")

	bundle, err := OpenCacheBundle(dir)
	assert.NilError(t, err)
	assert.Equal(t, dir, bundle.Dir)
	assert.Equal(t, writer.ID, bundle.ID)
	assert.Equal(t, 0, len(bundle.snapshot))
}

func TestCacheBundleOpenMissingManifest(t *testing.T) {
	t.Parallel()

	_, err := OpenCacheBundle(t.TempDir())
	assert.ErrorContains(t, err, "read cache bundle manifest")
}

func TestCacheBundleBlobPathRejectsUnsupportedDigest(t *testing.T) {
	t.Parallel()

	_, err := cacheBundleBlobPath(t.TempDir(), digest.Digest("sha512:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	assert.ErrorContains(t, err, "unsupported cache bundle digest algorithm")
}

func TestCacheBundleOpenRejectsEmptySnapshotRefKey(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "bundle-a")
	_, err := CreateCacheBundle(dir)
	assert.NilError(t, err)

	err = writeCacheBundleJSON(filepath.Join(dir, "snapshots", "index.json"), cacheBundleSnapshotIndex{
		Snapshots: []BundleSnapshot{{}},
	})
	assert.NilError(t, err)

	_, err = OpenCacheBundle(dir)
	assert.ErrorContains(t, err, "empty ref key")
}

func TestCacheBundleBlobPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dgst := digest.FromString("hello")
	path, err := cacheBundleBlobPath(dir, dgst)
	assert.NilError(t, err)
	assert.Equal(t, filepath.Join(dir, "blobs", "sha256", dgst.Encoded()), path)

	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o700))
}
