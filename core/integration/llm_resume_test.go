package core

// These tests cover intentionally restoring a portable LLM recipe in a
// different live session while the saving session still owns its cached
// workspace and host-read results. Persisted LLM resume opts into recipe
// replanning; ordinary ID references keep recorded-load behavior.

import (
	"context"
	"os"
	"path/filepath"

	"dagger.io/dagger"
	"github.com/dagger/testctx"
	"github.com/stretchr/testify/require"
)

func savedSessionConversation(c *dagger.Client, contents string) *dagger.LLM {
	return c.LLM().
		WithWorkspace(c.CurrentWorkspace()).
		WithModel("openai/gpt-4o").
		WithSystemPrompt("be helpful").
		WithPrompt("read x.txt").
		WithResponse([]dagger.LLMContentBlockInput{
			{Kind: dagger.LLMContentBlockKindText, Text: "reading x.txt"},
			{
				Kind:      dagger.LLMContentBlockKindToolCall,
				CallID:    "call_1",
				ToolName:  "read",
				Arguments: dagger.JSON(`{"path":"x.txt"}`),
			},
		}).
		WithToolResult("call_1", contents, false)
}

func saveAndResumeAlive(
	ctx context.Context,
	t *testctx.T,
	workdir string,
	autosave bool,
	build func(*dagger.Client) *dagger.LLM,
	prime func(*dagger.LLM),
) (*dagger.LLM, *dagger.Client) {
	t.Helper()
	cA := connect(ctx, t, dagger.WithWorkdir(workdir))
	llmA := build(cA)
	if prime != nil {
		prime(llmA)
	}
	toSave := llmA
	if !autosave {
		toSave = llmA.WithWorkspace(cA.CurrentWorkspace())
	}
	savedID, err := toSave.PortableID(ctx)
	require.NoError(t, err)

	// Keep cA alive through the resume. This is the high-value arrangement:
	// its recorded workspace remains cache-resident and would win a default
	// recipe digest lookup.
	cB := connect(ctx, t, dagger.WithWorkdir(workdir))
	return dagger.RefWithRecomputedImplicitInputs[*dagger.LLM](cB, savedID), cB
}

func overlayToolEdit(
	ctx context.Context,
	t *testctx.T,
	llm *dagger.LLM,
	before, after *dagger.Directory,
) *dagger.LLM {
	t.Helper()
	patch, err := after.Changes(before).AsPatch().Contents(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, patch)
	normalized := before.
		WithPatch(patch, dagger.DirectoryWithPatchOpts{OnConflict: dagger.PatchConflictLeaveConflictMarkers}).
		Changes(before)
	return llm.WithWorkspace(llm.Workspace().WithChanges(normalized))
}

func editorWrite(ctx context.Context, t *testctx.T, llm *dagger.LLM, path, contents string) *dagger.LLM {
	t.Helper()
	before := llm.Workspace().Directory(".")
	return overlayToolEdit(ctx, t, llm, before, before.WithNewFile(path, contents))
}

func editorEdit(ctx context.Context, t *testctx.T, llm *dagger.LLM, path, oldText, newText string) *dagger.LLM {
	t.Helper()
	before := llm.Workspace().Directory(".")
	after := before.WithFile(path, before.File(path).WithReplaced(oldText, newText))
	return overlayToolEdit(ctx, t, llm, before, after)
}

func conflictedFiles(ctx context.Context, t *testctx.T, llm *dagger.LLM, baseline *dagger.Workspace) []string {
	t.Helper()
	changes := llm.Workspace().Changes(dagger.WorkspaceChangesOpts{From: baseline})
	added, err := changes.AddedPaths(ctx)
	require.NoError(t, err)
	modified, err := changes.ModifiedPaths(ctx)
	require.NoError(t, err)
	paths := append(append([]string{}, added...), modified...)
	if len(paths) == 0 {
		return nil
	}
	results, err := changes.After().Search(ctx, "<<<<<<< workspace", dagger.DirectorySearchOpts{
		Literal: true, FilesOnly: true, Paths: paths,
	})
	require.NoError(t, err)
	var files []string
	seen := map[string]bool{}
	for _, result := range results {
		path, err := result.FilePath(ctx)
		require.NoError(t, err)
		if !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	return files
}

func (LLMSuite) TestResumeHostReadsInNewSession(ctx context.Context, t *testctx.T) {
	workdir := t.TempDir()
	initGitRepo(ctx, t, workdir)
	path := filepath.Join(workdir, "x.txt")
	require.NoError(t, os.WriteFile(path, []byte("resume-live ORIGINAL"), 0o644))

	resumed, _ := saveAndResumeAlive(ctx, t, workdir, false,
		func(cA *dagger.Client) *dagger.LLM {
			return savedSessionConversation(cA, "resume-live ORIGINAL")
		},
		func(llmA *dagger.LLM) {
			contents, err := llmA.Workspace().File("x.txt").Contents(ctx)
			require.NoError(t, err)
			require.Equal(t, "resume-live ORIGINAL", contents)
			_, err = llmA.Tools(ctx)
			require.NoError(t, err)
		})

	require.NoError(t, os.WriteFile(path, []byte("resume-live EDITED"), 0o644))
	reply, err := resumed.LastReply(ctx)
	require.NoError(t, err)
	require.Equal(t, "reading x.txt", reply)
	contents, err := resumed.Workspace().File("x.txt").Contents(ctx)
	require.NoError(t, err)
	require.Equal(t, "resume-live EDITED", contents,
		"resume must read the current session's live workspace")
	_, err = resumed.Tools(ctx)
	require.NoError(t, err,
		"tool derivation must use the resumed workspace client")
}

func (LLMSuite) TestResumeKeepsPendingEdits(ctx context.Context, t *testctx.T) {
	workdir := t.TempDir()
	initGitRepo(ctx, t, workdir)
	require.NoError(t, os.WriteFile(filepath.Join(workdir, "a.txt"),
		[]byte("pending one\nORIGINAL\npending three\n"), 0o644))

	resumed, resumedClient := saveAndResumeAlive(ctx, t, workdir, true,
		func(cA *dagger.Client) *dagger.LLM {
			llm := savedSessionConversation(cA, "pending one\nORIGINAL\npending three\n")
			llm = editorEdit(ctx, t, llm, "a.txt", "ORIGINAL", "EDITED")
			return editorWrite(ctx, t, llm, "b.txt", "pending BRAND NEW\n")
		}, nil)

	contents, err := resumed.Workspace().File("a.txt").Contents(ctx)
	require.NoError(t, err)
	require.Equal(t, "pending one\nEDITED\npending three\n", contents)
	created, err := resumed.Workspace().File("b.txt").Contents(ctx)
	require.NoError(t, err)
	require.Equal(t, "pending BRAND NEW\n", created)

	changes := resumed.Workspace().Changes(dagger.WorkspaceChangesOpts{From: resumedClient.CurrentWorkspace()})
	modified, err := changes.ModifiedPaths(ctx)
	require.NoError(t, err)
	require.Contains(t, modified, "a.txt")
	added, err := changes.AddedPaths(ctx)
	require.NoError(t, err)
	require.Contains(t, added, "b.txt")
	require.Empty(t, conflictedFiles(ctx, t, resumed, resumedClient.CurrentWorkspace()))
}

func (LLMSuite) TestResumeLeavesConflictMarkersForOutOfBandEdits(ctx context.Context, t *testctx.T) {
	workdir := t.TempDir()
	initGitRepo(ctx, t, workdir)
	path := filepath.Join(workdir, "a.txt")
	require.NoError(t, os.WriteFile(path,
		[]byte("conflict one\nORIGINAL\nconflict three\n"), 0o644))

	resumed, resumedClient := saveAndResumeAlive(ctx, t, workdir, true,
		func(cA *dagger.Client) *dagger.LLM {
			return editorEdit(ctx, t,
				savedSessionConversation(cA, "conflict one\nORIGINAL\nconflict three\n"),
				"a.txt", "ORIGINAL", "EDITED")
		},
		func(*dagger.LLM) {
			require.NoError(t, os.WriteFile(path,
				[]byte("conflict one\nCHANGED ON HOST\nconflict three\n"), 0o644))
		})

	contents, err := resumed.Workspace().File("a.txt").Contents(ctx)
	require.NoError(t, err)
	require.Contains(t, contents, "<<<<<<< workspace")
	require.Contains(t, contents, ">>>>>>> patch")
	require.Contains(t, contents, "CHANGED ON HOST")
	require.Contains(t, contents, "EDITED")
	require.Equal(t, []string{"a.txt"}, conflictedFiles(ctx, t, resumed, resumedClient.CurrentWorkspace()))
}

func (LLMSuite) TestResumeAppliesNonConflictingOutOfBandEdits(ctx context.Context, t *testctx.T) {
	workdir := t.TempDir()
	initGitRepo(ctx, t, workdir)
	path := filepath.Join(workdir, "a.txt")
	require.NoError(t, os.WriteFile(path,
		[]byte("first\nsecond\nthird\nfourth\nfifth\nsixth\nseventh\neighth\nninth\n"), 0o644))

	resumed, resumedClient := saveAndResumeAlive(ctx, t, workdir, true,
		func(cA *dagger.Client) *dagger.LLM {
			return editorEdit(ctx, t, savedSessionConversation(cA, "unused"),
				"a.txt", "second", "SECOND EDITED")
		},
		func(*dagger.LLM) {
			require.NoError(t, os.WriteFile(path,
				[]byte("first\nsecond\nthird\nfourth\nfifth\nsixth\nseventh\neighth\nNINTH ON HOST\n"), 0o644))
		})

	contents, err := resumed.Workspace().File("a.txt").Contents(ctx)
	require.NoError(t, err)
	require.Contains(t, contents, "SECOND EDITED")
	require.Contains(t, contents, "NINTH ON HOST")
	require.NotContains(t, contents, "<<<<<<< workspace")
	require.Empty(t, conflictedFiles(ctx, t, resumed, resumedClient.CurrentWorkspace()))
}
