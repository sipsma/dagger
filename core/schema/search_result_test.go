package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/dagql/call"
	"github.com/dagger/dagger/engine"
)

// searchResultScalarFields are read once per match of every search, so they
// must not leave a cache result each.
var searchResultScalarFields = map[string][]string{
	"SearchResult":   {"filePath", "lineNumber", "absoluteOffset", "matchedLines"},
	"SearchSubmatch": {"text", "start", "end"},
}

func TestSearchResultScalarFieldsNotCached(t *testing.T) {
	ctx := context.Background()
	cache, err := dagql.NewCache(ctx, "", nil, nil)
	require.NoError(t, err)
	ctx = dagql.ContextWithCache(ctx, cache)
	ctx = engine.ContextWithClientMetadata(ctx, &engine.ClientMetadata{ClientID: "search-result-fields", SessionID: "search-result-fields"})
	server := &currentTypeDefsTestServer{}
	root := core.NewRoot(server)
	ctx = core.ContextWithQuery(ctx, root)
	base, err := NewCoreSchemaBase(ctx, server)
	require.NoError(t, err)
	dag, err := base.Fork(ctx, root, "")
	require.NoError(t, err)
	server.dag = dag
	// This fixture has no module dependency resolver, and every ID belongs to
	// this schema, so node(id:) can use dagql's direct loader.
	dag.SetNodeLoader(nil)

	// The real search fields read a snapshot. What is under test is how the
	// results' fields are read, so return fixed results.
	const matches = 3
	dagql.Fields[*core.Query]{
		dagql.Func("__testSearch", func(context.Context, *core.Query, struct{}) (dagql.Array[*core.SearchResult], error) {
			results := make(dagql.Array[*core.SearchResult], 0, matches)
			for i := range matches {
				results = append(results, &core.SearchResult{
					FilePath:       "pkg/file.go",
					LineNumber:     i + 1,
					AbsoluteOffset: 10 * i,
					MatchedLines:   "func TestThing(t *testing.T) {\n",
					Submatches:     []*core.SearchSubmatch{{Text: "TestThing", Start: 5, End: 14}},
				})
			}
			return results, nil
		}),
	}.Install(dag)

	res, err := dag.Query(ctx, `{
		__testSearch {
			filePath lineNumber absoluteOffset matchedLines
			submatches { id text start end }
		}
	}`, nil)
	require.NoError(t, err)

	var out struct {
		Results []struct {
			FilePath       string
			LineNumber     int
			AbsoluteOffset int
			MatchedLines   string
			Submatches     []struct {
				ID    string
				Text  string
				Start int
				End   int
			}
		} `json:"__testSearch"`
	}
	resJSON, err := json.Marshal(res)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(resJSON, &out))
	require.Len(t, out.Results, matches)
	for i, result := range out.Results {
		require.Equal(t, "pkg/file.go", result.FilePath)
		require.Equal(t, i+1, result.LineNumber)
		require.Equal(t, 10*i, result.AbsoluteOffset)
		require.Equal(t, "func TestThing(t *testing.T) {\n", result.MatchedLines)
		require.Len(t, result.Submatches, 1)
		require.Equal(t, "TestThing", result.Submatches[0].Text)
		require.Equal(t, 5, result.Submatches[0].Start)
		require.Equal(t, 14, result.Submatches[0].End)
	}

	var snapshotJSON bytes.Buffer
	require.NoError(t, cache.WriteDebugCacheSnapshot(&snapshotJSON))
	var snapshot dagql.CacheDebugSnapshot
	require.NoError(t, json.Unmarshal(snapshotJSON.Bytes(), &snapshot))
	typeNames := make(map[uint64]string, len(snapshot.Results))
	for _, r := range snapshot.Results {
		typeNames[r.SharedResultID] = r.TypeName
	}
	cached := map[string]int{}
	for _, r := range snapshot.Results {
		if r.ResultCall == nil || r.ResultCall.Receiver == nil {
			continue
		}
		receiver := typeNames[r.ResultCall.Receiver.ResultID]
		for _, field := range searchResultScalarFields[receiver] {
			if r.ResultCall.Field == field {
				cached[receiver+"."+field]++
			}
		}
	}
	require.Empty(t, cached, "search result scalar fields stored in the cache, by field")

	// A submatch is an object: it keeps an ID that loads in a later request,
	// and its uncached fields read from the loaded object.
	submatchID := out.Results[0].Submatches[0].ID
	require.NotEmpty(t, submatchID)
	var id call.ID
	require.NoError(t, id.Decode(submatchID))
	loaded, err := dag.Load(ctx, &id)
	require.NoError(t, err)
	var text string
	require.NoError(t, dag.Select(ctx, loaded, &text, dagql.Selector{Field: "text"}))
	require.Equal(t, "TestThing", text)
}
