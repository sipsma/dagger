package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/util/parallel"
	"github.com/vektah/gqlparser/v2/ast"
)

// Check represents a validation check with its result
type Check struct {
	Node      *ModTreeNode `json:"node"`
	Completed bool         `field:"true" doc:"Whether the check completed"`
	Passed    bool         `field:"true" doc:"Whether the check passed"`

	Error dagql.Nullable[dagql.ObjectResult[*Error]] `field:"true" doc:"If the check failed, this is the error"`

	// IsGenerate indicates this check was derived from a +generate function.
	// When true, the check passes if the generator produces an empty changeset.
	IsGenerate bool
}

type CheckGroup struct {
	Node   *ModTreeNode `json:"node"`
	Checks []*Check     `json:"checks"`
}

var _ dagql.PersistedObject = (*Check)(nil)
var _ dagql.PersistedObjectDecoder = (*Check)(nil)
var _ dagql.HasDependencyResults = (*Check)(nil)
var _ dagql.PersistedObject = (*CheckGroup)(nil)
var _ dagql.PersistedObjectDecoder = (*CheckGroup)(nil)
var _ dagql.HasDependencyResults = (*CheckGroup)(nil)

type persistedCheckPayload struct {
	NodeID     int  `json:"nodeID,omitempty"`
	IsGenerate bool `json:"isGenerate,omitempty"`
}

type persistedCheckObjectPayload struct {
	Tree  persistedModTree      `json:"tree"`
	Check persistedCheckPayload `json:"check"`
}

type persistedCheckGroupPayload struct {
	Tree   persistedModTree        `json:"tree"`
	NodeID int                     `json:"nodeID,omitempty"`
	Checks []persistedCheckPayload `json:"checks,omitempty"`
}

func NewCheckGroup(ctx context.Context, mod dagql.ObjectResult[*Module], include []string, noGenerate, onlyGenerate bool) (*CheckGroup, error) {
	rootNode, err := NewModTree(ctx, mod)
	if err != nil {
		return nil, err
	}

	var checks []*Check
	if !onlyGenerate {
		checkNodes, err := rootNode.RollupChecks(ctx, include, nil)
		if err != nil {
			return nil, err
		}
		checks = make([]*Check, 0, len(checkNodes))
		for _, checkNode := range checkNodes {
			checks = append(checks, &Check{Node: checkNode})
		}
	}

	if !noGenerate {
		genNodes, err := rootNode.RollupGenerator(ctx, include, nil)
		if err != nil {
			return nil, err
		}
		// Build a set of existing check paths to avoid duplicates when a
		// function is annotated with both +check and +generate.
		checkPaths := make(map[string]struct{}, len(checks))
		for _, c := range checks {
			checkPaths[c.Name()] = struct{}{}
		}
		for _, genNode := range genNodes {
			if _, exists := checkPaths[genNode.PathString()]; !exists {
				checks = append(checks, &Check{Node: genNode, IsGenerate: true})
			}
		}
	}

	return &CheckGroup{
		Node:   rootNode,
		Checks: checks,
	}, nil
}

func (*CheckGroup) Type() *ast.Type {
	return &ast.Type{
		NamedType: "CheckGroup",
		NonNull:   true,
	}
}

func (r *CheckGroup) List() []*Check {
	return r.Checks
}

// Run all the checks in the group
func (r *CheckGroup) Run(ctx context.Context, failFast bool) (*CheckGroup, error) {
	r = r.Clone()

	jobs := parallel.New().WithContextualTracer(true).WithFailFast(failFast)
	for _, check := range r.Checks {
		// Reset output fields, in case we're re-running
		check.Completed = false
		check.Passed = false
		jobs = jobs.WithJob(check.Name(), func(ctx context.Context) error {
			var err error
			if check.IsGenerate {
				err = check.Node.RunGeneratorAsCheck(ctx, nil, nil)
			} else {
				err = check.Node.RunCheck(ctx, nil, nil)
			}
			check.Completed = true
			if err != nil {
				check.Passed = false
				errObj, errErr := NewErrorFromErr(ctx, err)
				if errErr != nil {
					return fmt.Errorf("create error from %w (%T): %w", err, err, errErr)
				}
				check.Error.Value = errObj
				check.Error.Valid = true
			} else {
				check.Passed = true
			}
			return err
		})
	}
	if err := jobs.Run(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *CheckGroup) Report(ctx context.Context) (dagql.ObjectResult[*File], error) {
	headers := []string{"check", "type", "description", "success"}
	rows := [][]string{}
	for _, check := range r.Checks {
		rows = append(rows, []string{
			check.Name(),
			check.CheckType(),
			check.Description(),
			check.ResultEmoji(),
		})
	}
	contents := markdownTable(headers, rows...)

	srv, err := CurrentDagqlServer(ctx)
	if err != nil {
		return dagql.ObjectResult[*File]{}, err
	}

	var file dagql.ObjectResult[*File]
	err = srv.Select(ctx, srv.Root(), &file,
		dagql.Selector{
			Field: "file",
			Args: []dagql.NamedInput{
				{Name: "name", Value: dagql.String("checks.md")},
				{Name: "contents", Value: dagql.String(contents)},
			},
		},
	)
	if err != nil {
		return dagql.ObjectResult[*File]{}, err
	}
	return file, nil
}

func markdownTable(headers []string, rows ...[]string) string {
	var sb strings.Builder
	sb.WriteString("| " + strings.Join(headers, " | ") + " |\n")
	for range headers {
		sb.WriteString("| -- ")
	}
	sb.WriteString("|\n")
	for _, row := range rows {
		sb.WriteString("|" + strings.Join(row, " | ") + " |\n")
	}
	return sb.String()
}

func (r *CheckGroup) Clone() *CheckGroup {
	cp := *r
	if cp.Node != nil {
		cp.Node = cp.Node.Clone()
	}
	cp.Checks = make([]*Check, len(r.Checks))
	for i := range cp.Checks {
		cp.Checks[i] = r.Checks[i].Clone()
	}
	return &cp
}

func (c *Check) Path() []string {
	return c.Node.Path()
}

func (c *Check) Description() string {
	return c.Node.Description
}

func (c *Check) OriginalModule() *Module {
	return c.Node.OriginalModule.Self()
}

func (*Check) Type() *ast.Type {
	return &ast.Type{
		NamedType: "Check",
		NonNull:   true,
	}
}

func (c *Check) ResultEmoji() string {
	if c.Completed {
		if c.Passed {
			return "🟢"
		}
		return "🔴"
	}
	return ""
}

func (c *Check) Name() string {
	return c.Node.PathString()
}

func (c *Check) CheckType() string {
	if c.IsGenerate {
		return "generate"
	}
	return "check"
}

func (c *Check) Clone() *Check {
	cp := *c
	cp.Node = c.Node.Clone()
	return &cp
}

func encodePersistedCheckPayload(
	tree *persistedModTreeEncoder,
	c *Check,
) (persistedCheckPayload, error) {
	if c == nil {
		return persistedCheckPayload{}, fmt.Errorf("encode persisted check: nil check")
	}
	nodeID, err := tree.Add(c.Node)
	if err != nil {
		return persistedCheckPayload{}, err
	}
	return persistedCheckPayload{
		NodeID:     nodeID,
		IsGenerate: c.IsGenerate,
	}, nil
}

func decodePersistedCheckPayload(
	nodes map[int]*ModTreeNode,
	payload persistedCheckPayload,
) (*Check, error) {
	if payload.NodeID == 0 {
		return nil, fmt.Errorf("decode persisted check: missing node ID")
	}
	node, ok := nodes[payload.NodeID]
	if !ok {
		return nil, fmt.Errorf("decode persisted check: unknown node ID %d", payload.NodeID)
	}
	return &Check{
		Node:       node,
		IsGenerate: payload.IsGenerate,
	}, nil
}

func (c *Check) EncodePersistedObject(ctx context.Context, cache dagql.PersistedObjectCache) (dagql.PersistedObjectEncoding, error) {
	_ = ctx
	tree := newPersistedModTreeEncoder(cache)
	checkPayload, err := encodePersistedCheckPayload(tree, c)
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	payload, err := json.Marshal(persistedCheckObjectPayload{
		Tree:  tree.tree,
		Check: checkPayload,
	})
	if err != nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("marshal persisted check payload: %w", err)
	}
	return encodePersistedObjectRawJSON(payload), nil
}

func (*Check) DecodePersistedObject(
	ctx context.Context,
	dag *dagql.Server,
	_ uint64,
	_ *dagql.ResultCall,
	payload json.RawMessage,
) (dagql.Typed, error) {
	var persisted persistedCheckObjectPayload
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode persisted check payload: %w", err)
	}
	nodes, err := decodePersistedModTree(ctx, dag, persisted.Tree)
	if err != nil {
		return nil, err
	}
	return decodePersistedCheckPayload(nodes, persisted.Check)
}

func (c *Check) AttachDependencyResults(
	ctx context.Context,
	_ dagql.AnyResult,
	attach func(dagql.AnyResult) (dagql.AnyResult, error),
) ([]dagql.AnyResult, error) {
	_ = ctx
	if c == nil {
		return nil, nil
	}
	return attachModTreeNodeDependencyResults(c.Node, attach)
}

func (r *CheckGroup) EncodePersistedObject(ctx context.Context, cache dagql.PersistedObjectCache) (dagql.PersistedObjectEncoding, error) {
	_ = ctx
	if r == nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("encode persisted check group: nil check group")
	}
	tree := newPersistedModTreeEncoder(cache)
	nodeID, err := tree.Add(r.Node)
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	checkPayloads := make([]persistedCheckPayload, 0, len(r.Checks))
	for _, check := range r.Checks {
		checkPayload, err := encodePersistedCheckPayload(tree, check)
		if err != nil {
			return dagql.PersistedObjectEncoding{}, err
		}
		checkPayloads = append(checkPayloads, checkPayload)
	}
	payload, err := json.Marshal(persistedCheckGroupPayload{
		Tree:   tree.tree,
		NodeID: nodeID,
		Checks: checkPayloads,
	})
	if err != nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("marshal persisted check group payload: %w", err)
	}
	return encodePersistedObjectRawJSON(payload), nil
}

func (*CheckGroup) DecodePersistedObject(
	ctx context.Context,
	dag *dagql.Server,
	_ uint64,
	_ *dagql.ResultCall,
	payload json.RawMessage,
) (dagql.Typed, error) {
	var persisted persistedCheckGroupPayload
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode persisted check group payload: %w", err)
	}
	nodes, err := decodePersistedModTree(ctx, dag, persisted.Tree)
	if err != nil {
		return nil, err
	}
	var node *ModTreeNode
	if persisted.NodeID != 0 {
		var ok bool
		node, ok = nodes[persisted.NodeID]
		if !ok {
			return nil, fmt.Errorf("decode persisted check group: unknown node ID %d", persisted.NodeID)
		}
	}
	checks := make([]*Check, 0, len(persisted.Checks))
	for _, checkPayload := range persisted.Checks {
		check, err := decodePersistedCheckPayload(nodes, checkPayload)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return &CheckGroup{
		Node:   node,
		Checks: checks,
	}, nil
}

func (r *CheckGroup) AttachDependencyResults(
	ctx context.Context,
	_ dagql.AnyResult,
	attach func(dagql.AnyResult) (dagql.AnyResult, error),
) ([]dagql.AnyResult, error) {
	_ = ctx
	if r == nil {
		return nil, nil
	}
	owned, err := attachModTreeNodeDependencyResults(r.Node, attach)
	if err != nil {
		return nil, err
	}
	for _, check := range r.Checks {
		checkDeps, err := check.AttachDependencyResults(ctx, nil, attach)
		if err != nil {
			return nil, err
		}
		owned = append(owned, checkDeps...)
	}
	return owned, nil
}

func (c *Check) Run(ctx context.Context) (*Check, error) {
	c = c.Clone()

	var err error
	if c.IsGenerate {
		err = c.Node.RunGeneratorAsCheck(ctx, nil, nil)
	} else {
		err = c.Node.RunCheck(ctx, nil, nil)
	}
	c.Completed = true
	if err != nil {
		c.Passed = false
		errObj, errErr := NewErrorFromErr(ctx, err)
		if errErr != nil {
			return nil, fmt.Errorf("create error from %w (%T): %w", err, err, errErr)
		}
		c.Error.Value = errObj
		c.Error.Valid = true
	} else {
		c.Passed = true
	}
	return c, nil
}
