package core

import (
	"context"
	"strings"
	"testing"

	"github.com/dagger/dagger/dagql"
	"gotest.tools/v3/assert"
)

func TestCheckEncodePersistedObjectRoundTripsStructureAndResetsRunState(t *testing.T) {
	t.Parallel()

	root := &ModTreeNode{}
	node := &ModTreeNode{
		Parent:      root,
		Name:        "lint",
		Description: "run lint",
		IsCheck:     true,
	}
	check := &Check{
		Node:      node,
		Completed: true,
		Passed:    true,
		Error:     dagql.Nullable[dagql.ObjectResult[*Error]]{Valid: true},
	}

	encoding, err := check.EncodePersistedObject(context.Background(), nil)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "completed"))
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "passed"))
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "error"))

	decoded, err := new(Check).DecodePersistedObject(context.Background(), nil, 0, nil, encoding.JSON)
	assert.NilError(t, err)
	decodedCheck := decoded.(*Check)
	assert.Equal(t, decodedCheck.Name(), "lint")
	assert.Equal(t, decodedCheck.Description(), "run lint")
	assert.Assert(t, decodedCheck.Node.IsCheck)
	assert.Equal(t, decodedCheck.Completed, false)
	assert.Equal(t, decodedCheck.Passed, false)
	assert.Equal(t, decodedCheck.Error.Valid, false)
}

func TestCheckGroupEncodePersistedObjectRoundTripsChecksAndResetsRunState(t *testing.T) {
	t.Parallel()

	root := &ModTreeNode{Description: "root module"}
	checkNode := &ModTreeNode{
		Parent:      root,
		Name:        "unit",
		Description: "unit tests",
		IsCheck:     true,
	}
	generateNode := &ModTreeNode{
		Parent:      root,
		Name:        "generate",
		Description: "generated files are current",
		IsGenerator: true,
	}
	group := &CheckGroup{
		Node: root,
		Checks: []*Check{
			{
				Node:      checkNode,
				Completed: true,
				Passed:    true,
				Error:     dagql.Nullable[dagql.ObjectResult[*Error]]{Valid: true},
			},
			{
				Node:       generateNode,
				Completed:  true,
				IsGenerate: true,
			},
		},
	}

	encoding, err := group.EncodePersistedObject(context.Background(), nil)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "completed"))
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "passed"))
	assert.Assert(t, !strings.Contains(string(encoding.JSON), "error"))

	decoded, err := new(CheckGroup).DecodePersistedObject(context.Background(), nil, 0, nil, encoding.JSON)
	assert.NilError(t, err)
	decodedGroup := decoded.(*CheckGroup)
	assert.Equal(t, decodedGroup.Node.Description, "root module")
	assert.Equal(t, len(decodedGroup.Checks), 2)

	decodedCheck := decodedGroup.Checks[0]
	assert.Equal(t, decodedCheck.Name(), "unit")
	assert.Equal(t, decodedCheck.Description(), "unit tests")
	assert.Assert(t, decodedCheck.Node.IsCheck)
	assert.Equal(t, decodedCheck.Completed, false)
	assert.Equal(t, decodedCheck.Passed, false)
	assert.Equal(t, decodedCheck.Error.Valid, false)
	assert.Equal(t, decodedCheck.IsGenerate, false)

	decodedGenerate := decodedGroup.Checks[1]
	assert.Equal(t, decodedGenerate.Name(), "generate")
	assert.Equal(t, decodedGenerate.Description(), "generated files are current")
	assert.Assert(t, decodedGenerate.Node.IsGenerator)
	assert.Equal(t, decodedGenerate.Completed, false)
	assert.Equal(t, decodedGenerate.Passed, false)
	assert.Equal(t, decodedGenerate.Error.Valid, false)
	assert.Equal(t, decodedGenerate.IsGenerate, true)
}
