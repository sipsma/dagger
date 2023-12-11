package core

import (
	"testing"

	"dagger.io/dagger"
	"github.com/stretchr/testify/require"
)

func TestModuleGoIfaces(t *testing.T) {
	t.Parallel()

	c, ctx := connect(t)

	ctr := c.Container().From(golangImage).
		WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
		WithWorkdir("/work/mallard").
		WithNewFile("./main.go", dagger.ContainerWithNewFileOpts{
			Contents: `package main

type Mallard struct {}

func (m *Mallard) Quack() string {
	return "mallard quack"
}
			`,
		}).
		With(daggerExec("mod", "init", "--name=mallard", "--sdk=go")).
		WithWorkdir("/work/crested").
		WithNewFile("./main.go", dagger.ContainerWithNewFileOpts{
			Contents: `package main

type Crested struct {}

func (m *Crested) Quack() string {
	return "crested quack"
}
			`,
		}).
		With(daggerExec("mod", "init", "--name=crested", "--sdk=go")).
		WithWorkdir("/work/pond").
		WithNewFile("./main.go", dagger.ContainerWithNewFileOpts{
			Contents: `package main
import (
	"context"
	"strings"
)

type Pond struct {
	Quacks []string
}

type Duck interface {
	DaggerObject
	Quack(context.Context) (string, error)
}

func (m Pond) WithDuck(ctx context.Context, duck Duck) (*Pond, error) {
	quack, err := duck.Quack(ctx)
	if err != nil {
		return nil, err
	}
	m.Quacks = append(m.Quacks, quack)
	return &m, nil
}

func (m Pond) WithMaybeDuck(ctx context.Context, maybeDuck Optional[Duck]) (*Pond, error) {
	duck, ok := maybeDuck.Get()
	if !ok {
		return &m, nil
	}
	quack, err := duck.Quack(ctx)
	if err != nil {
		return nil, err
	}
	m.Quacks = append(m.Quacks, quack)
	return &m, nil
}

func (m Pond) WithDucks(ctx context.Context, ducks []Duck) (*Pond, error) {
	for _, duck := range ducks {
		quack, err := duck.Quack(ctx)
		if err != nil {
			return nil, err
		}
		m.Quacks = append(m.Quacks, quack)
	}
	return &m, nil
}

func (m *Pond) QuackAll() string {
	return strings.Join(m.Quacks, "\n")
}
			`,
		}).
		With(daggerExec("mod", "init", "--name=pond", "--sdk=go")).
		WithWorkdir("/work").
		WithNewFile("./main.go", dagger.ContainerWithNewFileOpts{
			Contents: `package main

import (
	"context"
)

type Top struct{}

func (m *Top) Test(ctx context.Context) (string, error) {
	return dag.Pond().
		WithDuck(dag.Mallard()).
		WithDuck(dag.Crested()).
		QuackAll(ctx)
}

func (m *Top) TestList(ctx context.Context) (string, error) {
	return dag.Pond().
		WithDucks([]PondDuck{dag.Mallard(), dag.Crested()}).
		QuackAll(ctx)
}

func (m *Top) TestOptional(ctx context.Context) (string, error) {
	return dag.Pond().
		WithMaybeDuck().
		WithDuck(dag.Crested()).
		QuackAll(ctx)
}
			`,
		}).
		With(daggerExec("mod", "init", "--name=top", "--sdk=go")).
		With(daggerExec("mod", "install", "./mallard")).
		With(daggerExec("mod", "install", "./crested")).
		With(daggerExec("mod", "install", "./pond"))

	t.Run("basic", func(t *testing.T) {
		t.Parallel()
		out, err := ctr.With(daggerQuery(`{top{test}}`)).Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"top":{"test":"mallard quack\ncrested quack"}}`, out)
	})

	t.Run("list", func(t *testing.T) {
		t.Parallel()
		out, err := ctr.With(daggerQuery(`{top{testList}}`)).Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"top":{"testList":"mallard quack\ncrested quack"}}`, out)
	})

	t.Run("optional", func(t *testing.T) {
		t.Parallel()
		out, err := ctr.With(daggerQuery(`{top{testOptional}}`)).Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"top":{"testOptional":"crested quack"}}`, out)
	})

}
