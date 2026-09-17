package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"dagger.io/dagger"
	"github.com/dagger/dagger/internal/buildkit/identity"
	"github.com/dagger/dagger/internal/testutil"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

type ModuleDefinitionSuite struct{}

type servedObject struct {
	AsObject struct {
		Name      string
		Functions []struct{ Name string }
	}
}

type served struct {
	Objects []servedObject
	Runtime struct{ ID string }
}

func functionNamesOf(objects []servedObject) []string {
	var names []string
	for _, obj := range objects {
		for _, fn := range obj.AsObject.Functions {
			names = append(names, obj.AsObject.Name+"."+fn.Name)
		}
	}
	return names
}

func TestModuleDefinitionSuite(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(ModuleDefinitionSuite{})
}

const moduleDefinitionProbeSource = `package main

type Probe struct{}

func (m *Probe) Hello() string { return "hello" }
`

const moduleDefinitionProbeSourceEdited = moduleDefinitionProbeSource + `
func (m *Probe) Bye() string { return "bye" }
`

// A Go module's type definitions are a cached, content-keyed result: the
// first client's load computes them through the runtime, a second client's
// load on the same engine hits that row, a source edit makes a new one, and
// a module whose runtime is not a container takes the uncached path.
func (ModuleDefinitionSuite) TestCachedAcrossClients(ctx context.Context, t *testctx.T) {
	outer := connect(ctx, t)
	fixture := outer.CacheVolume("module-definition-fixture-" + identity.NewID())
	engineCtr := devEngineContainerWithStateKey(outer, "module-definition-state-"+identity.NewID(), func(ctr *dagger.Container) *dagger.Container {
		return ctr.WithMountedCache("/transfer-fixture", fixture).WithEnvVariable("_DAGGER_TEST_REMOTE_CACHE_FIXTURE_ROOT", "/transfer-fixture")
	})
	// Retained rows must outlive the sessions that made them for the row
	// counts below to mean anything, independently of the host's disk
	// pressure; the predecessor's two-engine test pins the same bounds.
	engineCtr = engineWithConfig(ctx, t, engineConfigWithEnabled(true), engineConfigWithGC("1000000000000000", "0", "1000000000000000", "0"))(engineCtr)
	engine := devEngineContainerAsService(engineCtr)
	tunnel, err := outer.Host().Tunnel(engine).Start(ctx)
	require.NoError(t, err)
	defer func() {
		_, _ = tunnel.Stop(ctx, dagger.ServiceStopOpts{Kill: true})
		_, _ = engine.Stop(ctx)
	}()
	endpoint, err := tunnel.Endpoint(ctx, dagger.ServiceEndpointOpts{Scheme: "tcp"})
	require.NoError(t, err)

	checkout := t.TempDir()
	writeProbe := func(source string) {
		require.NoError(t, os.MkdirAll(filepath.Join(checkout, ".dagger"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(checkout, "dagger.json"), []byte(`{"name":"probe","engineVersion":"latest","sdk":{"source":"go"},"source":".dagger"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(checkout, ".dagger", "main.go"), []byte(source), 0o644))
	}
	writeProbe(moduleDefinitionProbeSource)

	// load connects a fresh client to the engine with the checkout as its
	// working directory, serves the module, reads the served definition and
	// the runtime, and returns the cache's rows for the _moduleDefinition
	// field as the fixture reports them.
	load := func(t *testctx.T, dir string) (served, []uint64) {
		t.Helper()
		client, err := dagger.Connect(ctx, dagger.WithRunnerHost(endpoint), dagger.WithWorkdir(dir), dagger.WithLogOutput(testutil.NewTWriter(t)))
		require.NoError(t, err)
		defer func() { require.NoError(t, client.Close()) }()
		require.NoError(t, client.ModuleSource(".").AsModule().Serve(ctx))
		var data struct {
			ModuleSource struct{ AsModule served }
		}
		require.NoError(t, client.Do(ctx, &dagger.Request{Query: `{moduleSource(refString:"."){asModule{objects{asObject{name functions{name}}} runtime{id}}}}`}, &dagger.Response{Data: &data}))
		var report transferFixtureReport
		require.NoError(t, transferFixture(ctx, client, "report", "", []string{}, &report))
		var definitions []uint64
		for _, row := range report.Rows {
			if row.Call != nil && row.Call.Field == "_moduleDefinition" {
				definitions = append(definitions, row.ResultID)
				args, _ := json.Marshal(row.Call.Args)
				t.Logf("definition row %d imported=%t persisted=%t receiver=%v args=%s", row.ResultID, row.Imported, row.Persisted, row.Call.Receiver, args)
			}
		}
		t.Logf("served %v runtime=%t definitions=%v", functionNamesOf(data.ModuleSource.AsModule.Objects), data.ModuleSource.AsModule.Runtime.ID != "", definitions)
		return data.ModuleSource.AsModule, definitions
	}
	functionNames := func(s served) []string { return functionNamesOf(s.Objects) }

	first, firstDefs := load(t, checkout)
	require.Len(t, firstDefs, 1, "the first load computed one definition")
	require.Equal(t, []string{"Probe.hello"}, functionNames(first))
	require.NotEmpty(t, first.Runtime.ID, "Module.runtime resolves on the computing client")

	second, secondDefs := load(t, checkout)
	require.Equal(t, firstDefs, secondDefs, "the second client hit the same definition row and computed none")
	require.Equal(t, functionNames(first), functionNames(second), "the served definitions are equal")
	require.NotEmpty(t, second.Runtime.ID, "Module.runtime resolves on the hitting client")

	writeProbe(moduleDefinitionProbeSourceEdited)
	third, thirdDefs := load(t, checkout)
	require.ElementsMatch(t, []string{"Probe.hello", "Probe.bye"}, functionNames(third), "the edited source's definition is served")
	require.Len(t, thirdDefs, 2, "a source edit is a new definition beside the retained old one")
	require.Contains(t, thirdDefs, firstDefs[0])

	// A Dang module's runtime is native, not a container: its load takes the
	// uncached path and creates no definition row.
	dangDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dangDir, "dagger.json"), []byte(`{"name":"native","engineVersion":"latest","sdk":{"source":"dang"}}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dangDir, "main.dang"), []byte("type Native {\n  hello: String! { \"hi\" }\n}\n"), 0o644))
	native, nativeDefs := load(t, dangDir)
	require.Equal(t, []string{"Native.hello"}, functionNames(native))
	require.ElementsMatch(t, thirdDefs, nativeDefs, "no definition row was added for a non-container runtime")
}
