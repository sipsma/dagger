package core

import (
	"context"
	"errors"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/stretchr/testify/require"
)

type failingCallServer struct{ mockServer }

func (*failingCallServer) CurrentFunctionCall(context.Context) (*FunctionCall, error) {
	return nil, errors.New("client scope lookup failed")
}

func TestModuleProcessModule(t *testing.T) {
	t.Parallel()

	moduleFor := func(t *testing.T, fnCall *FunctionCall) bool {
		t.Helper()
		query := &Query{Server: &mockServer{moduleSource: &ModuleSource{ModuleName: "app"}, functionCall: fnCall}}
		mod, err := ModuleProcessModule(ContextWithQuery(t.Context(), query))
		require.NoError(t, err)
		return mod.Self() != nil
	}

	t.Run("a call that runs a module process passes its module", func(t *testing.T) {
		t.Parallel()
		fnCall := newFunctionCall(FunctionCall{Name: "build"})
		fnCall.SetModuleProcess()
		require.True(t, moduleFor(t, fnCall))
	})

	t.Run("a call of a container runtime does not", func(t *testing.T) {
		t.Parallel()
		require.False(t, moduleFor(t, newFunctionCall(FunctionCall{Name: "build"})))
	})

	t.Run("a client outside a function call does not, such as a module process", func(t *testing.T) {
		t.Parallel()
		require.False(t, moduleFor(t, nil))
	})

	t.Run("a failed call lookup fails", func(t *testing.T) {
		t.Parallel()
		_, err := ModuleProcessModule(ContextWithQuery(t.Context(), &Query{Server: &failingCallServer{}}))
		require.ErrorContains(t, err, "client scope lookup failed")
	})
}

// The exec that runs a module's process for the function call its client
// answers, keyed to the call by the call's digest in its environment as an
// SDK entrypoint's dispatcher exec is, carries that call's read scope to the
// process's requests, as ContainerRuntime.Call's exec of the call does: the
// process answers the call and runs whenever the call does. Any other exec
// carries what reads made under its own call count as: one the entrypoint
// keys otherwise, as a helper that a later run of the call can be served
// from the cache, or any exec of a client whose call starts no process.
func TestExecReadScopeOfAModuleProcess(t *testing.T) {
	t.Parallel()
	// The function call's reads count, as a session-bound call's at a
	// session's top do; under the withExec, a cached call, reads don't.
	const callDigest = "xxh3:00000000c0ffee00"
	fnCall := newFunctionCall(FunctionCall{Name: "check"})
	fnCall.SetModuleProcess()
	fnCall.readScope = dagql.CurrentReadScope(t.Context())
	fnCall.callDigest = callDigest
	query := &Query{Server: &mockServer{moduleSource: &ModuleSource{ModuleName: "app"}, functionCall: fnCall}}
	ctx := dagql.ContextWithCallerRecipe(ContextWithQuery(t.Context(), query), "xxh3:0123456789abcdef")
	underExec := dagql.CurrentReadScope(ctx)
	require.NotEqual(t, fnCall.ReadScope(), underExec, "the two scopes differ")
	mod, err := ModuleProcessModule(ctx)
	require.NoError(t, err)
	require.NotNil(t, mod.Self(), "the exec serves the call's module")
	dispatcher := &Container{}
	dispatcher.Config.Env = []string{"PATH=/bin", "DAGGER_MODULE_CALL_DIGEST=" + callDigest}
	helper := &Container{}
	helper.Config.Env = []string{"PATH=/bin"}

	require.Equal(t, fnCall.ReadScope(), execReadScope(ctx, dispatcher, mod, nil), "the module's process answers the call")
	require.Equal(t, underExec, execReadScope(ctx, helper, mod, nil), "a helper exec not keyed to the call")
	var none dagql.ObjectResult[*Module]
	require.Equal(t, underExec, execReadScope(ctx, dispatcher, none, nil), "an exec serving no module")
	require.Equal(t, underExec, execReadScope(ctx, dispatcher, mod, newFunctionCall(FunctionCall{Name: "other"})), "an exec run for a call it is given")

	undigested := newFunctionCall(FunctionCall{Name: "check"})
	undigested.SetModuleProcess()
	undigested.readScope = fnCall.ReadScope()
	other := dagql.ContextWithCallerRecipe(ContextWithQuery(t.Context(), &Query{Server: &mockServer{moduleSource: &ModuleSource{ModuleName: "app"}, functionCall: undigested}}), "xxh3:0123456789abcdef")
	require.Equal(t, underExec, execReadScope(other, &Container{}, mod, nil), "a call with no digest keys no process")

	runtime := newFunctionCall(FunctionCall{Name: "check"})
	runtime.readScope = fnCall.ReadScope()
	runtime.callDigest = callDigest
	other = dagql.ContextWithCallerRecipe(ContextWithQuery(t.Context(), &Query{Server: &mockServer{moduleSource: &ModuleSource{ModuleName: "app"}, functionCall: runtime}}), "xxh3:0123456789abcdef")
	require.Equal(t, underExec, execReadScope(other, dispatcher, mod, nil), "a client whose call starts no process of its own")
}
