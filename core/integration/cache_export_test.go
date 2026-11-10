package core

import (
	"context"
	"crypto/rand"
	"encoding/json"

	"dagger.io/dagger"
	"dagger.io/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/distconsts"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
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

// The engine image ships dagger/go's checkout, the tree the Go SDK's
// dependency loads from, with its data: the image's build exports dagger/go's
// module source, and the export carries what the source holds, the checkout
// among it, with no case for module sources.
func (CacheSuite) TestImageEntriesShipTheGoSDKCheckout(ctx context.Context, t *testctx.T) {
	c := connect(ctx, t)
	raw, err := devEngineContainer(c).File(distconsts.EngineContainerImageEntriesDir + "/" + dagql.BundleFile).Contents(ctx)
	require.NoError(t, err)
	var bundle dagql.ValueBundle
	require.NoError(t, json.Unmarshal([]byte(raw), &bundle))
	trees := map[dagql.TransferOrdinal]bool{}
	for _, value := range bundle.Values {
		if call := value.Record.Call; call != nil && call.Field == "tree" {
			trees[value.Ordinal] = true
		}
	}
	require.NotEmpty(t, trees, "the bundle holds a Git tree")
	shipped := false
	for _, output := range bundle.Outputs {
		if trees[output.Ordinal] && output.Address.Part == "snapshot" && (output.Chain != nil || output.Prefix != nil) {
			shipped = true
		}
	}
	require.True(t, shipped, "a Git tree's snapshot ships with its data")
}
