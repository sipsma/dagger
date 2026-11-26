//go:build linux
// +build linux

package snapshots

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"github.com/containerd/continuity/sysx"
	"github.com/dagger/dagger/engine/snapshots/fsdiff"
	"golang.org/x/sys/unix"
)

// Unchanged reports whether the snapshot holds no change against the one it
// was prepared on, so that committing it would make a second snapshot of
// its parent's filesystem. Under a snapshotter that layers each snapshot's
// own directory over its parent's, that directory then holds no entry, and
// its root has the owner, mode and extended attributes of the directory
// below it. Their times are not compared: a prepared snapshot's root has
// the time it was prepared, the snapshot's layer leaves the root out, and
// content digests leave times out. So a file made and removed again at the
// root leaves the snapshot unchanged, and one made and removed in a
// directory below it, which the directory's copy holds, does not.
//
// It reads one entry of the directory and the roots' attributes, whatever
// the snapshot holds. A snapshot prepared on no parent, or under a
// snapshotter that copies its parent (native), reports false.
func (sr *mutableRef) Unchanged(ctx context.Context) (bool, error) {
	mountable, err := sr.cm.Snapshotter.Mounts(ctx, sr.SnapshotID())
	if err != nil {
		return false, err
	}
	mounts, release, err := mountable.Mount()
	if err != nil {
		return false, err
	}
	if release != nil {
		defer release()
	}
	if len(mounts) != 1 || !fsdiff.IsOverlayMountType(mounts[0]) {
		return false, nil
	}
	layers, err := fsdiff.GetOverlayLayers(mounts[0])
	if err != nil {
		return false, err
	}
	if len(layers) < 2 {
		return false, nil
	}
	upper, below := layers[len(layers)-1], layers[len(layers)-2]
	if !emptyDir(upper) {
		return false, nil
	}
	return sameRoot(upper, below)
}

// sameRoot reports whether the snapshot's own directory upper shows the
// owner, mode and extended attributes of below, the directory of the layer
// under it, and isn't opaque. The overlay filesystem's own attributes are
// its bookkeeping, not the directory's.
func sameRoot(upper, below string) (bool, error) {
	var su, sb unix.Stat_t
	if err := unix.Lstat(upper, &su); err != nil {
		return false, err
	}
	if err := unix.Lstat(below, &sb); err != nil {
		return false, err
	}
	if su.Mode != sb.Mode || su.Uid != sb.Uid || su.Gid != sb.Gid {
		return false, nil
	}
	xu, err := rootXattrs(upper)
	if err != nil {
		return false, err
	}
	for _, opaque := range []string{"trusted.overlay.opaque", "user.overlay.opaque"} {
		if bytes.Equal(xu[opaque], []byte("y")) {
			return false, nil
		}
	}
	xb, err := rootXattrs(below)
	if err != nil {
		return false, err
	}
	overlays := func(name string) bool {
		return strings.HasPrefix(name, "trusted.overlay.") || strings.HasPrefix(name, "user.overlay.")
	}
	for name, value := range xu {
		if !overlays(name) && !bytes.Equal(value, xb[name]) {
			return false, nil
		}
	}
	for name := range xb {
		if _, ok := xu[name]; !ok && !overlays(name) {
			return false, nil
		}
	}
	return true, nil
}

// rootXattrs returns the extended attributes of path, by name.
func rootXattrs(path string) (map[string][]byte, error) {
	names, err := sysx.LListxattr(path)
	if err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			return nil, nil
		}
		return nil, err
	}
	attrs := make(map[string][]byte, len(names))
	for _, name := range names {
		value, err := sysx.LGetxattr(path, name)
		if errors.Is(err, unix.ENODATA) {
			continue
		}
		if err != nil {
			return nil, err
		}
		attrs[name] = value
	}
	return attrs, nil
}
