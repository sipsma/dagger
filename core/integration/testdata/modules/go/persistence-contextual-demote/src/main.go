package main

import (
	"dagger/test/internal/dagger"
)

type Test struct{}

// Data returns the contextual directory itself, so serving a cached call
// must materialize the directory's content — the content-addressed,
// upload-shaped row the persistence demote test targets.
func (*Test) Data(
	// +defaultPath="/data"
	dir *dagger.Directory,
) *dagger.Directory {
	return dir
}
