//go:build linux
// +build linux

package snapshots

import (
	"bytes"
	"context"
	"errors"
	"slices"
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
	return sameRoot(upper, below, overlayXattrPrefix(mounts[0].Options))
}

// overlayXattrPrefix is the prefix of the extended attributes an overlay
// mount with options keeps for itself: its user namespace's with userxattr,
// the trusted one's otherwise.
func overlayXattrPrefix(options []string) string {
	if slices.Contains(options, "userxattr") {
		return "user.overlay."
	}
	return "trusted.overlay."
}

// sameRoot reports whether the snapshot's own directory upper shows the
// owner, mode and extended attributes of below, the directory of the layer
// under it, and isn't opaque. The overlay mount's own attributes, those
// under prefix, are its bookkeeping, not the directory's; one under
// prefix+"overlay." is an escaped one a process set, and is the
// directory's.
func sameRoot(upper, below, prefix string) (bool, error) {
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
	if bytes.Equal(xu[prefix+"opaque"], []byte("y")) {
		return false, nil
	}
	xb, err := rootXattrs(below)
	if err != nil {
		return false, err
	}
	private := func(name string) bool {
		return strings.HasPrefix(name, prefix) && !strings.HasPrefix(name, prefix+"overlay.")
	}
	for name, value := range xu {
		if private(name) {
			continue
		}
		if was, ok := xb[name]; !ok || !bytes.Equal(value, was) {
			return false, nil
		}
	}
	for name := range xb {
		if _, ok := xu[name]; !ok && !private(name) {
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
