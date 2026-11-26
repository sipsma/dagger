//go:build !linux
// +build !linux

package snapshots

import "context"

// Unchanged reports false: only Linux's overlay snapshotter layers a
// snapshot's own directory over its parent's.
func (sr *mutableRef) Unchanged(context.Context) (bool, error) {
	return false, nil
}
