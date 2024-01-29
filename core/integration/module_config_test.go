package core

import (
	"encoding/json"
	"strings"
	"testing"

	"dagger.io/dagger"
	"github.com/dagger/dagger/core/modules"
	"github.com/stretchr/testify/require"
)

/*
	TODO: more tests:

* dagger mod init --name=test when there's already a module named test at subdir ./test and in the current dir's dagger.json
* variations of above when doing init when there's already a dagger.json w/ root-for settings
*/
func TestModuleSourceConfigs(t *testing.T) {
	// test dagger.json source configs that aren't inherently covered in other tests

	t.Parallel()
	c, ctx := connect(t)

	t.Run("upgrade from old config", func(t *testing.T) {
		t.Parallel()

		baseWithOldConfig := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work/foo").
			With(daggerExec("mod", "init", "--name=dep", "--sdk=go")).
			WithWorkdir("/work").
			With(daggerExec("mod", "init", "--name=test", "--sdk=go")).
			WithNewFile("/work/dagger.json", dagger.ContainerWithNewFileOpts{
				Contents: `{"name": "test", "sdk": "go", "include": ["*"], "exclude": ["bar"], "dependencies": ["foo"]}`,
			})

		// verify sync updates config to new format
		confContents, err := baseWithOldConfig.With(daggerExec("mod", "sync")).File("dagger.json").Contents(ctx)
		require.NoError(t, err)
		expectedConf := modules.ModuleConfig{
			Name:    "test",
			SDK:     "go",
			Include: []string{"*"},
			Exclude: []string{"bar"},
			Dependencies: []*modules.ModuleConfigDependency{{
				Name:   "dep",
				Source: "foo",
			}},
			RootFor: []*modules.ModuleConfigRootFor{{
				Source: ".",
			}},
		}
		expectedConfBytes, err := json.Marshal(expectedConf)
		require.NoError(t, err)
		require.JSONEq(t, strings.TrimSpace(string(expectedConfBytes)), confContents)

		// verify call works seamlessly even without explicit sync yet
		out, err := baseWithOldConfig.With(daggerCall("container-echo", "--string-arg", "hey")).Stdout(ctx)
		require.NoError(t, err)
		require.Equal(t, "hey", strings.TrimSpace(out))
	})

	t.Run("old config with root fails", func(t *testing.T) {
		t.Parallel()

		out, err := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("mod", "init", "--name=test", "--sdk=go")).
			WithNewFile("/work/dagger.json", dagger.ContainerWithNewFileOpts{
				Contents: `{"name": "test", "sdk": "go", "root": ".."}`,
			}).
			With(daggerCall("container-echo", "--string-arg", "hey")).
			Stdout(ctx)
		require.Error(t, err)
		require.Contains(t, `Cannot load module config with legacy "root" setting`, out)
	})

	t.Run("dep has separate config", func(t *testing.T) {
		// Verify that if a local dep has its own dagger.json, that's used to load it correctly.
		t.Parallel()

		base := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work/subdir/dep").
			With(daggerExec("mod", "init", "--name=dep", "--sdk=go")).
			WithNewFile("/work/subdir/dep/main.go", dagger.ContainerWithNewFileOpts{
				Contents: `package main

			import "context"

			type Dep struct {}

			func (m *Dep) DepFn(ctx context.Context, str string) string { return str }
			`,
			}).
			WithWorkdir("/work").
			With(daggerExec("mod", "init", "-m=test", "--name=test", "--sdk=go")).
			With(daggerExec("mod", "install", "-m=test", "./subdir/dep")).
			WithNewFile("/work/test/main.go", dagger.ContainerWithNewFileOpts{
				Contents: `package main

			import "context"

			type Test struct {}

			func (m *Test) Fn(ctx context.Context) (string, error) { return dag.Dep().DepFn(ctx, "hi dep") }
			`,
			})

		// try invoking it from a few different paths, just for more corner case coverage

		t.Run("from src dir", func(t *testing.T) {
			t.Parallel()
			out, err := base.WithWorkdir("test").With(daggerCall("fn")).Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from src root", func(t *testing.T) {
			t.Parallel()
			out, err := base.With(daggerCallAt("test", "fn")).Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from root", func(t *testing.T) {
			t.Parallel()
			out, err := base.WithWorkdir("/").With(daggerCallAt("work/test", "fn")).Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from dep parent", func(t *testing.T) {
			t.Parallel()
			out, err := base.WithWorkdir("/work/subdir").With(daggerCallAt("../test", "fn")).Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from dep dir", func(t *testing.T) {
			t.Parallel()
			out, err := base.WithWorkdir("/work/subdir/dep").With(daggerCallAt("../../test", "fn")).Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})
	})

	t.Run("install dep from weird places", func(t *testing.T) {
		t.Parallel()

		base := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("mod", "init", "-m=subdir/dep", "--name=dep", "--sdk=go")).
			WithNewFile("/work/subdir/dep/main.go", dagger.ContainerWithNewFileOpts{
				Contents: `package main

			import "context"

			type Dep struct {}

			func (m *Dep) DepFn(ctx context.Context, str string) string { return str }
			`,
			}).
			With(daggerExec("mod", "init", "-m=test", "--name=test", "--sdk=go")).
			WithNewFile("/work/test/main.go", dagger.ContainerWithNewFileOpts{
				Contents: `package main

			import "context"

			type Test struct {}

			func (m *Test) Fn(ctx context.Context) (string, error) { return dag.Dep().DepFn(ctx, "hi dep") }
			`,
			})

		t.Run("from src dir", func(t *testing.T) {
			// sanity test normal case
			t.Parallel()
			out, err := base.
				WithWorkdir("/work/test").
				With(daggerExec("mod", "install", "../subdir/dep")).
				With(daggerCall("fn")).
				Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from root", func(t *testing.T) {
			t.Parallel()
			out, err := base.
				WithWorkdir("/").
				With(daggerExec("mod", "install", "-m=./work/test", "./work/subdir/dep")).
				WithWorkdir("/work/test").
				With(daggerCall("fn")).
				Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from dep", func(t *testing.T) {
			t.Parallel()
			out, err := base.
				WithWorkdir("/work/subdir/dep").
				With(daggerExec("mod", "install", "-m=../../test", ".")).
				WithWorkdir("/work/test").
				With(daggerCall("fn")).
				Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})

		t.Run("from random place", func(t *testing.T) {
			t.Parallel()
			out, err := base.
				WithWorkdir("/var").
				With(daggerExec("mod", "install", "-m=../work/test", "../work/subdir/dep")).
				WithWorkdir("/work/test").
				With(daggerCall("fn")).
				Stdout(ctx)
			require.NoError(t, err)
			require.Equal(t, "hi dep", strings.TrimSpace(out))
		})
	})

	t.Run("install out of tree dep fails", func(t *testing.T) {
		t.Parallel()

		base := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work/dep").
			With(daggerExec("mod", "init", "--name=dep", "--sdk=go")).
			WithWorkdir("/work/test").
			With(daggerExec("mod", "init", "--name=test", "--sdk=go"))

		t.Run("from src dir", func(t *testing.T) {
			t.Parallel()
			_, err := base.
				WithWorkdir("/work/test").
				With(daggerExec("mod", "install", "../dep")).
				Sync(ctx)
			require.ErrorContains(t, err, `module dep source path "../dep" escapes root "/"`)
		})

		t.Run("from dep dir", func(t *testing.T) {
			t.Parallel()
			_, err := base.
				WithWorkdir("/work/dep").
				With(daggerExec("mod", "install", "-m=../test", ".")).
				Sync(ctx)
			require.ErrorContains(t, err, `module dep source path "../dep" escapes root "/"`)
		})

		t.Run("from root", func(t *testing.T) {
			t.Parallel()
			_, err := base.
				WithWorkdir("/").
				With(daggerExec("mod", "install", "-m=work/test", "work/dep")).
				Sync(ctx)
			require.ErrorContains(t, err, `module dep source path "../dep" escapes root "/"`)
		})
	})

	t.Run("malicious config", func(t *testing.T) {
		// verify a maliciously/incorrectly constructed dagger.json is still handled correctly
		t.Parallel()

		base := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work/dep").
			With(daggerExec("mod", "init", "--name=dep", "--sdk=go")).
			WithNewFile("/work/dep/main.go", dagger.ContainerWithNewFileOpts{
				Contents: `package main

			import "context"

			type Dep struct {}

			func (m *Dep) GetSource(ctx context.Context) *Directory { 
				return dag.CurrentModule().Source()
			}
			`,
			}).
			WithWorkdir("/work").
			With(daggerExec("mod", "init", "--name=test", "--sdk=go"))

		t.Run("no root for", func(t *testing.T) {
			t.Parallel()

			base := base.
				With(configFile("/work/dep", &modules.ModuleConfig{
					Name: "dep",
					SDK:  "go",
				}))

			out, err := base.With(daggerCallAt("dep", "get-source", "entries")).Stdout(ctx)
			require.NoError(t, err)
			// shouldn't default the root dir to /work just because we called it from there,
			// it should default to just using dep's source dir in this case
			ents := strings.Fields(strings.TrimSpace(out))
			require.Equal(t, []string{
				".gitattributes",
				"LICENSE",
				"dagger.gen.go",
				"dagger.json",
				"go.mod",
				"go.sum",
				"main.go",
				"querybuilder",
				// no "dep" dir
			}, ents)
		})

		t.Run("dep points out of root", func(t *testing.T) {
			t.Parallel()

			base := base.
				With(configFile(".", &modules.ModuleConfig{
					Name: "evil",
					SDK:  "go",
					Dependencies: []*modules.ModuleConfigDependency{{
						Name:   "escape",
						Source: "..",
					}},
					RootFor: []*modules.ModuleConfigRootFor{{
						Source: ".",
					}},
				}))

			_, err := base.With(daggerCall("container-echo", "--string-arg", "plz fail")).Sync(ctx)
			require.ErrorContains(t, err, `module dep source path ".." escapes root "/"`)

			_, err = base.With(daggerExec("mod", "sync")).Sync(ctx)
			require.ErrorContains(t, err, `module dep source path ".." escapes root "/"`)

			_, err = base.With(daggerExec("mod", "install", "./dep")).Sync(ctx)
			require.ErrorContains(t, err, `module dep source path ".." escapes root "/"`)
		})
	})
}