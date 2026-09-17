# Design: a cached, content-keyed module definition

Engine seat, 17 September 2026. Lines cited at `ae53c6ac13` on branch
`engine-seat-0bf270c0`. Status: design for review, no code.

## Goal

A cold engine's module load should not need the runtime container's root
filesystem. Measured on engine B in the demo (`TestDemo`, 21:12 run): of a
5.08 s load, 4.68 s is the download of the runtime's 225 MB root filesystem,
demanded by the one exec that reads the module's type definitions. With the
definition cached and imported, B's load is the per-client resolvers, about
0.4 s.

## Today's path, and what the definition depends on

`ModuleSource.asModule` is per client (`core/schema/modulesource.go:243`,
`PerClientInput`). Its resolver builds the `Module` value, loads the
dependencies, computes `AsModuleVariantDigest` (`:3659`), and calls
`runModuleDefInSDK` (`:3180`). For an SDK without the `moduleTypes`
capability, which the Go SDK is (`core/sdk/go_sdk.go:68`), that calls
`moduleDefViaRuntime` (`:3296`): it loads the runtime container (`:3305`,
a chain of cached dagql calls, hits on B), sets `mod.Runtime` (`:3310`),
and runs the runtime once with an empty function name (`:3337`) through
`ModuleFunction.Call` (`core/modfunc.go:835`) and
`ContainerRuntime.Call` (`core/sdk.go:245`), whose `WithExec` (`:290`) is
made under `dagql.WithSkip` and is not a dagql call. The module binary
answers by creating `TypeDef`, `Function`, `SourceMap` and `Module` results
through its nested client and returning a `Module` ID; the returned
`Module` value (`initialized`) is patched into `mod` (`:3222` to `:3245`).

What the definition is a function of, and whether each is in the key:

| Input | Where it enters today | In the key of the new call |
| --- | --- | --- |
| The module's source content, name, subpaths, SDK source, include paths, dependency digests, blueprint, toolchains, config clients, user defaults | `SourceImplementationDigest` (`core/modulesource.go:1406` to `:1497`), the content digest of `ModuleSource._implementationScoped` (`core/schema/modulesource.go:3450`) | Yes, through the receiver: the implementation-scoped source. |
| The dependencies' schema, the introspection JSON the runtime is built with | `Query.__schemaJSONFile` (`core/schema/query.go:32`), persistable, keyed by `CurrentSchemaInput` and the default platform; the Go SDK mounts it into the runtime (`core/schema_build.go:157`) | Yes, as an argument: the file's ID. |
| The SDK implementation and the engine version | The runtime container's chain: `_builtinContainer` of the engine's own SDK image, codegen and build (plan section 4.6) | Yes, as an argument: the runtime container's ID. Any SDK or engine change changes that chain. |
| The module name as loaded, which `LegacyNameOverride` can change (`:3670`) before the SDK runs and which the SDK stamps into the definitions | Not in `SourceImplementationDigest` (it hashes `ModuleOriginalName`) | Yes, as a string argument. |
| `AsModuleVariantDigest` (`:3659`): default-path context, forced function caching, legacy name, workspace config, dot-env defaults, arg customizations | Salts the final module identity; the workspace and customization parts are applied after the SDK ran (`:3697` to `:3712`) | No. They post-process the definition per client and stay per client. The name is covered above. |
| Client ID, session, workspace | `PerClientInput` on `asModule` and `moduleSource`; the exec's `execMD.CallDigest` is the per-client call's digest (`core/modfunc.go:857`) | Must not be. The new call has no per-client input; its exec's `CallDigest` becomes the new call's own digest. |
| Eager runtime (`engine/opts.go:114`) | `:3256`, after the definition | No; it is an action, not an input. |

## Proposed shape

One new internal field, `ModuleSource._moduleDefinition`, declared beside
`_implementationScoped` (`core/schema/modulesource.go:246`), `NodeFunc`,
`IsPersistable()`, no per-client input:

```
_moduleDefinition(runtime: ContainerID!, introspectionJson: FileID!, moduleName: String!): Module!
```

- Receiver: the implementation-scoped module source, the one `asModule`
  already obtains through `ImplementationScopedModuleSource`
  (`core/modulesource.go:1493`). Its result carries the content digest with
  the remote-cache label, so A's imported row and B's own row share one
  eq-class; that is the mechanism that already gives B the `cacheDemo` hit
  (H3 diagnosis note, evidence item 2).
- Arguments: ID arguments are result references (`ResultCallLiteralKindResultRef`,
  `dagql/result_call_frame.go:152`) and enter the structural term as input
  classes the way the receiver and the module reference do, so the match is
  structural: B's runtime container and introspection file are hits on A's
  imported rows (the 30 cached runtime-chain calls in the 21:12 run, and
  `__schemaJSONFile`, retained and imported), so their classes are the same
  rows. If either misses on B, the definition misses too and B runs the exec,
  which is today's behavior, only slower.
- Result: a `Module` value holding only the definition: `Description`,
  `ObjectDefs`, `InterfaceDefs`, `EnumDefs`, and `Runtime` set to the
  argument container so the persisted module keeps its runtime reference.
  This is what `moduleDefViaRuntime` returns today plus the runtime.
  `Module` already has a persisted encoding with references for the typedef
  results and the runtime (`core/module.go:1005` to `:1110`), so the
  definition travels in a bundle as an ordinary row whose dependencies are
  the `TypeDef` rows, exactly as A's module row does today.
- Resolver: the body of `moduleDefViaRuntime` minus the runtime load, or,
  for an SDK with `moduleTypes`, the body of `ModuleTypes`
  (`core/sdk/module_typedefs.go:23`), which is already a chain of cached
  dagql calls on the SDK module. The resolver retains the returned module's
  rows as the moduleTypes path does with `AddExplicitDependency`
  (`core/sdk/module_typedefs.go:165`), so the definition owns its typedefs.

`runModuleDefInSDK` then becomes: load the runtime as today (`:3305`, cached
chain, no download) and set `mod.Runtime`; select `_moduleDefinition` on the
scoped source with the runtime's ID, the deps' introspection file ID and the
module name; patch the returned definition into `mod` as today. What still
runs per client: `moduleSource`, the dependency loads, `_implementationScoped`,
the runtime chain lookups (hits), `asModule`'s own body, the legacy
post-processing, `Module.serve`. That is the 0.4 s measured.

## What can go wrong

- Stale definitions after a source change: the receiver's content digest
  changes with the source (`SourceImplementationDigest` hashes the context
  directory's digest), the runtime container's ID changes with it, so the
  key changes. Same invalidation as function calls have today.
- `ErrStaleSDKCapability` (`core/modulesource.go:644`, `:3202`): a source
  persisted with a `moduleTypes` capability the loaded SDK no longer has
  falls to the runtime path. The new call sits below that decision, so the
  fallback still works; the resolver dispatches on the capability again.
- Non-Go SDKs (`AsModuleTypes`, `core/sdk/module.go:270`, Dang
  `core/sdk/dang_sdk.go:148`): already cached dagql chains. Routing them
  through the same field adds one persistable row above them and changes
  nothing else; or they stay as they are and only the runtime path uses the
  field. Recommended: same field for both, one place to reason about.
- Self calls (`:3247`): `IncludeSelfInDeps` is set from the source's
  self-calls flag after the definition, unchanged.
- Eager runtime (`:3256`): with `mod.Runtime` set from the definition, the
  `!mod.Runtime.Valid` guard would skip the forced `sync` that fills the
  cache. Keep the sync when `EagerRuntime` is set regardless of the guard.
- `Module.runtime` (`core/schema/module.go:2104`): returns `mod.Runtime`
  when valid, else loads it; unchanged.
- References inside the definition: `TypeDef` results and their `SourceMap`
  results are the definition row's dependencies and relocate through the
  bundle like today's typedef rows (A's `build` bundle already carried
  `TypeDef.__withObjectTypeDef`, `SourceMap.sourceMap` and friends). The
  `Runtime` reference relocates like the module's today. Nothing new must
  travel.
- The per-client `asModule` still creates a fresh `Module` row per client
  whose typedefs are the imported ones; that is what happens today for the
  scoped module row, and `cacheDemo` hits through it.
- Two clients loading the same module at once on one engine run the exec
  twice if the singleflight does not join them; the persistable call's
  concurrency key joins identical calls, as for every other field.

## How it is tested

- In-process, `core/schema`: install the field on a dagql server with a
  scripted resolver counter, select it twice from two client contexts with
  the same scoped source, runtime ID and file ID, and require one resolver
  run and one row; select it with a changed `moduleName` and require a
  second run. This proves the key, not the exec.
- Engine test, `core/integration` (through `dagger api call engine-dev
  test`, one selection): a Go module loaded by two clients on one engine;
  the engine logs one `module definition computed` line (a new info line in
  the resolver) for the two loads, and the second load's `asModule` span
  has no `asModule getModDef` child. Bounded timeout as the rules require.
- Cross-engine: the demo run by the service seat; B's load time and the
  absence of the 225 MB `fs` download line are the acceptance.

## Export side

The definition row must reach B inside A's bundle. It is persistable, so it
has a retention edge on A, but as a leaf it would need its own export
command. Better: the final module keeps a reference to its definition row
(`Module.Definition`, persisted as `DefinitionResultID` beside
`RuntimeResultID`, `core/module.go:1010`), so the definition is inside the
closure of every module-object leaf, exported with `build`'s bundle, and
imported with it. The runtime chain stays in the closure through the
definition's `Runtime` reference and the module's own, so the service seat's
runtime-parts upload is unchanged. A definition that some client computed
but no leaf references remains a retained leaf and exports on its own; that
is harmless and cheap (metadata only, no parts).
