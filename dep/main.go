package main

import (
	"context"
	"dagger/dep/internal/dagger"
)

type Dep struct {
	// TODO: could try making this //+private, unknown if user is on this one
	Dir *dagger.Directory

	// TODO: could try making this //+private, unknown if user is on this one
	Ref *dagger.GitRef
}

func New(
	dir *dagger.Directory,
) *Dep {
	return &Dep{
		Dir: dir,
	}
}

func (m *Dep) WithRef(
	ctx context.Context,
	ref *dagger.GitRef,
) (*Dep, error) {
	m.Ref = ref
	return m, nil
}

// TODO:
// - return container?
// - include/disclude something from git ref?
func (m *Dep) Fn(
	ctx context.Context,
) (*dagger.Directory, error) {
	return m.Ref.Tree(), nil
}
