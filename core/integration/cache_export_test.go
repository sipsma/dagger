package core

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"dagger.io/dagger"
	"dagger.io/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The cache export carries every part of a result, whatever its type: an
// exec's container brings its image config, its filesystem and the exec's
// metadata, which the export reads from the result's own record, with no
// list of parts per type.
func (CacheSuite) TestCacheExportCarriesEveryPart(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	ctr := core.NewQuery(c).Container().From(alpineImage).
		WithExec([]string{"sh", "-c", "echo " + rand.Text() + " > /out"})
	id, err := ctr.ID(ctx)
	require.NoError(t, err)
	var res struct {
		Engine struct {
			LocalCache struct {
				Export struct {
					File struct {
						Contents string `json:"contents"`
					} `json:"file"`
				} `json:"_export"`
			} `json:"localCache"`
		} `json:"engine"`
	}
	require.NoError(t, c.Do(ctx, &dagger.Request{
		Query:     `query($id: ID!, $bundle: String!) { engine { localCache { _export(results: [$id]) { file(path: $bundle) { contents } } } } }`,
		Variables: map[string]any{"id": id, "bundle": dagql.BundleFile},
	}, &dagger.Response{Data: &res}))
	raw := res.Engine.LocalCache.Export.File.Contents
	var bundle dagql.ValueBundle
	require.NoError(t, json.Unmarshal([]byte(raw), &bundle))
	require.Len(t, bundle.Roots, 1)
	root := bundle.Roots[0].Ordinal
	parts := map[string]bool{}
	for _, output := range bundle.Outputs {
		if output.Ordinal == root && len(output.Address.OutputPath) == 0 {
			parts[string(output.Address.Part)] = true
		}
	}
	require.Equal(t, map[string]bool{"metadata": true, "fs": true, "execMeta": true}, parts)
}

// A cache export carries a result's own data, and what made it as records
// only: a directory an exec wrote brings its snapshot, but not the exec
// container's filesystem, which a lookup of the directory never reads.
func (CacheSuite) TestCacheExportLeavesWhatMadeAResultAsRecords(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	dir := core.NewQuery(c).Container().From(alpineImage).
		WithExec([]string{"sh", "-c", "mkdir /out && echo " + rand.Text() + " > /out/f"}).
		Directory("/out")
	id, err := dir.ID(ctx)
	require.NoError(t, err)
	var res struct {
		Engine struct {
			LocalCache struct {
				Export struct {
					File struct {
						Contents string `json:"contents"`
					} `json:"file"`
				} `json:"_export"`
			} `json:"localCache"`
		} `json:"engine"`
	}
	require.NoError(t, c.Do(ctx, &dagger.Request{
		Query:     `query($id: ID!, $bundle: String!) { engine { localCache { _export(results: [$id]) { file(path: $bundle) { contents } } } } }`,
		Variables: map[string]any{"id": id, "bundle": dagql.BundleFile},
	}, &dagger.Response{Data: &res}))
	var bundle dagql.ValueBundle
	require.NoError(t, json.Unmarshal([]byte(res.Engine.LocalCache.Export.File.Contents), &bundle))
	require.Len(t, bundle.Roots, 1)
	root := bundle.Roots[0].Ordinal
	execs := 0
	for _, value := range bundle.Values {
		if call := value.Record.Call; call != nil && call.Field == "withExec" {
			execs++
		}
	}
	require.NotZero(t, execs, "the exec that made the directory travels as a record")
	// A part another value shares layers with may be named as a prefix of
	// the directory's chain, which carries no data of its own; only a chain
	// carries layers.
	for _, output := range bundle.Outputs {
		if output.Chain != nil && len(output.Chain.Layers) > 0 {
			require.Equal(t, root, output.Ordinal, "only the directory's own part carries data, not %s of value %d", output.Address.Part, output.Ordinal)
		}
	}
}

// Exporting a Git module source carries its checkout's data: the checkout is
// a result the source's value holds, so the export carries it with no case for
// module sources. Only the source is exported, so nothing else selects a part
// the checkout's data could arrive through.
func (CacheSuite) TestCacheExportCarriesAModuleSourceCheckout(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	ref := "github.com/dagger/dagger-test-modules/top-level@" + vcsTestCaseCommit
	var res struct {
		Engine struct {
			LocalCache struct {
				Export struct {
					File struct {
						Contents string `json:"contents"`
					} `json:"file"`
				} `json:"_export"`
			} `json:"localCache"`
		} `json:"engine"`
	}
	var src struct {
		ModuleSource struct {
			ID string `json:"id"`
		} `json:"moduleSource"`
	}
	require.NoError(t, c.Do(ctx, &dagger.Request{
		Query:     `query($ref: String!) { moduleSource(refString: $ref) { id } }`,
		Variables: map[string]any{"ref": ref},
	}, &dagger.Response{Data: &src}))
	require.NoError(t, c.Do(ctx, &dagger.Request{
		Query:     `query($id: ID!, $bundle: String!) { engine { localCache { _export(results: [$id]) { file(path: $bundle) { contents } } } } }`,
		Variables: map[string]any{"id": src.ModuleSource.ID, "bundle": dagql.BundleFile},
	}, &dagger.Response{Data: &res}))
	var bundle dagql.ValueBundle
	require.NoError(t, json.Unmarshal([]byte(res.Engine.LocalCache.Export.File.Contents), &bundle))
	require.Len(t, bundle.Roots, 1)
	var rootRecord []byte
	for _, value := range bundle.Values {
		if value.Ordinal == bundle.Roots[0].Ordinal {
			raw, err := json.Marshal(value.Record)
			require.NoError(t, err)
			rootRecord = raw
		}
	}
	require.NotEmpty(t, rootRecord, "the bundle holds the module source's record")
	// The source's record names its checkout by its ordinal in the bundle.
	checkout := findJSONKey(gjson.ParseBytes(rootRecord), "gitUnfilteredContextDirResultID")
	require.True(t, checkout.Exists(), "the module source's record names its checkout: %s", rootRecord)
	carried := false
	for _, output := range bundle.Outputs {
		if uint64(output.Ordinal) == checkout.Uint() && output.Address.Part == "snapshot" && (output.Chain != nil || output.Prefix != nil) {
			carried = true
		}
	}
	require.True(t, carried, "the checkout's snapshot ships with its data")
}

// findJSONKey returns the first value under key anywhere in v, decoding JSON
// strings that hold JSON on the way, as a record's payloads are.
func findJSONKey(v gjson.Result, key string) gjson.Result {
	var found gjson.Result
	var walk func(v gjson.Result)
	walk = func(v gjson.Result) {
		if found.Exists() {
			return
		}
		switch {
		case v.IsObject():
			v.ForEach(func(k, child gjson.Result) bool {
				if k.String() == key {
					found = child
					return false
				}
				walk(child)
				return !found.Exists()
			})
		case v.IsArray():
			v.ForEach(func(_, child gjson.Result) bool {
				walk(child)
				return !found.Exists()
			})
		case v.Type == gjson.String && gjson.Valid(v.String()):
			if inner := gjson.Parse(v.String()); inner.IsObject() || inner.IsArray() {
				walk(inner)
			}
		}
	}
	walk(v)
	return found
}
