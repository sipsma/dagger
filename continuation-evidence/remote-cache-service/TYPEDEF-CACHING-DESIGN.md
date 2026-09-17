# Design: a cached, content-keyed module definition

Engine seat, 17 September 2026. Lines cited at `ae53c6ac13` on branch
`engine-seat-0bf270c0` (unchanged by the note commits). Status: revision 2
after the design review of `2b723e36e5`; no code.

## Goal

A cold engine's module load should not need the runtime container's root
filesystem. Measured on engine B in the demo (`TestDemo`, 21:12 run): of a
5.08 s load, 4.68 s is the download of the runtime's 225 MB root filesystem,
demanded by the one exec that reads the module's type definitions. With the
definition cached and imported, B's load is the per-client resolvers, about
0.4 s. The scope is the container-based fallback that the Go SDK takes; the
`moduleTypes` capability path is left as it is (review finding B1).

## Today's path, and what the definition depends on

`ModuleSource.asModule` is per client (`core/schema/modulesource.go:243`,
`PerClientInput`). Its resolver builds the `Module` value, loads the
dependencies, computes `AsModuleVariantDigest` (`:3659`), and calls
`runModuleDefInSDK` (`:3180`). An SDK with the `moduleTypes` capability
discovers the definition through `ModuleTypes` (`:3198` to `:3210`), a chain
of cached dagql calls on the SDK module (`core/sdk/module_typedefs.go:23`),
with `ErrStaleSDKCapability` (`core/modulesource.go:644`, `:3202`) sending a
source persisted with a capability its SDK no longer has to the runtime
path. The Go SDK has no `moduleTypes` (`core/sdk/go_sdk.go:68`), so it takes
`moduleDefViaRuntime` (`:3296`): load the runtime (`:3305`, a chain of
cached dagql calls, hits on B), set `mod.Runtime` (`:3310`), run the runtime
once with an empty function name (`:3337`) through `ModuleFunction.Call`
(`core/modfunc.go:835`) and `ContainerRuntime.Call` (`core/sdk.go:245`),
whose `WithExec` (`:290`) runs under `dagql.WithSkip` and is not a dagql
call. The module binary answers by creating `TypeDef`, `Function`,
`SourceMap` and `Module` results through its nested client and returning a
`Module` ID; the returned value (`initialized`) is patched into `mod`
(`:3222` to `:3245`), then legacy workspace defaults and argument
customizations are applied per client (`:3697` to `:3712`).

What the container-path definition is a function of, and whether each is in
the key of the new call:

| Input | Where it enters today | In the key |
| --- | --- | --- |
| Source content, original name, subpaths, SDK source string, include paths, dependency digests, blueprint, toolchains, config clients, user defaults | `SourceImplementationDigest` (`core/modulesource.go:1406` to `:1497`), the content digest of `ModuleSource._implementationScoped` (`core/schema/modulesource.go:3450`, exported as a remote-cache-labeled extra, `:3459`) | Yes, through the receiver: the implementation-scoped source. |
| The dependencies' schema, the introspection JSON the runtime is built with | `Query.__schemaJSONFile` (`core/schema/query.go:32`), persistable, keyed on the schema, its view, the scrub arguments and the platform (`core/schema_build.go:138`); the core view is the module's configured engine version (`core/schema/modulesource.go:3889`) | Yes, as an argument: the file's ID. |
| The SDK implementation | The runtime chain's base is `_builtinContainer` keyed on the Go SDK image's manifest digest (`core/sdk/go_sdk.go:706`), then codegen and build | Yes, as an argument: the runtime container's ID. A different SDK image gives a different chain. The running engine's version is not itself an input; it reaches the key only through the SDK image it ships and the schema view above. |
| The module name as loaded: `mod.NameField`, which `LegacyNameOverride` replaces (`:3671`); `src.ModuleName` is not changed by the override | Not in `SourceImplementationDigest`, which hashes `ModuleOriginalName` | Yes, as a string argument, taken from `mod.NameField` after the override. |
| `AsModuleVariantDigest` (`:3659`): default-path context, forced function caching, legacy name, workspace config, dot-env defaults, arg customizations | Salts the final module identity; the workspace and customization parts are applied after discovery (`:3697` to `:3712`) | No. They post-process the definition per client. The name is covered above. |
| Client ID, session, workspace | `PerClientInput` on `asModule` and `moduleSource`; the exec's `execMD.CallDigest` is the per-client call's digest (`core/modfunc.go:857`) | Not as inputs of the new call. One client-dependent value remains inside the receiver's digest and is an existing limitation, not removed here: `SourceImplementationDigest` expands the user defaults (`core/modulesource.go:1431`), and that expansion can read the current client's environment (`core/envfile.go:149`, `core/host.go:36`). Two engines with equal files but different expanded defaults get different scoped digests and miss, as they do today for every SDK operation and function call. `SDK.Debug` randomizes the digest on purpose (`:1425`). |
| Eager runtime (`engine/opts.go:114`) | `:3256`, after discovery | No; it is an action, not an input, and stays where it is. |

The key is claimed sound for the container path only. `SourceImplementationDigest`
hashes `SDK.Source`, not the whole SDK configuration (`:1450`); an in-memory
experimental-feature change does not edit the context directory
(`core/schema/modulesource.go:2441`) yet Dang v1 reads that flag during
discovery (`core/sdk/dang/v1/sdk.go:53`). That is one reason the
`moduleTypes` path is not routed through the new field.

## Proposed shape

One new internal field, `ModuleSource._moduleDefinition`, declared beside
`_implementationScoped` (`core/schema/modulesource.go:246`), `NodeFunc`,
`IsPersistable()`, no per-client input:

```
_moduleDefinition(runtime: ContainerID!, introspectionJson: FileID!, moduleName: String!): Module!
```

- Receiver: the implementation-scoped module source, obtained through
  `ImplementationScopedModuleSource` (`core/modulesource.go:1493`). Its
  result exports its content digest, so A's imported row and B's own row
  share one eq-class; that mechanism already gives B the `cacheDemo` hit
  (H3 diagnosis note, evidence item 2).
- Arguments: ID arguments are result references
  (`ResultCallLiteralKindResultRef`, `dagql/result_call_frame.go:152`) and
  enter the structural term as input classes
  (`appendResultCallLiteralSelfRefs`, `:1400`), which the lookup canonicalizes
  (`dagql/cache_egraph.go:903`). Exact result numbers need not match. On B
  the runtime container and the introspection file were hits on A's imported
  rows in the 21:12 run (the 30 cached runtime-chain calls and
  `__schemaJSONFile`). If an upstream lookup misses and recomputes an
  equivalent input, the definition still hits; if it produces an inequivalent
  input, the definition misses and B runs the exec, which is today's
  behavior; an upstream error propagates as today.
- Result: a fresh, definition-only `Module` value created with
  `NewObjectResultForCurrentCall`, so its recorded call is `_moduleDefinition`
  and not the SDK result's own frame (an already attached result keeps its
  earlier frame, `dagql/cache_egraph.go:1708`, and transfer rebuilds
  identity from the recorded frame, `dagql/cache_value_import.go:314`). It
  holds `Description`, `ObjectDefs`, `InterfaceDefs`, `EnumDefs`, and
  `Runtime` set to the argument container. `Module`'s persisted encoding
  already references typedef results and the runtime (`core/module.go:1005`
  to `:1110`), with visitors for both (`core/persisted_visitors.go:502`);
  `SourceMap` holds scalar locations (`core/typedef.go:2601`).
- Resolver: the body of `moduleDefViaRuntime` from the `getModDef` span on
  (`:3313` to `:3345`), taking the runtime from the argument; it retains the
  SDK's returned module rows with `AddExplicitDependency` as the
  `moduleTypes` path does (`core/sdk/module_typedefs.go:165`), so the
  definition row owns its typedefs. One info log line, `module definition
  computed`, with the scoped digest, for the tests.

`moduleDefViaRuntime` then becomes: load the runtime as today (`:3305`) and
set `mod.Runtime` (`:3310`); if the runtime is not a container (Dang's is
native, `core/sdk/dang/v2/sdk.go:94` returns false), run the exec uncached
exactly as today; otherwise select `_moduleDefinition` on the scoped source
with the runtime's ID, the deps' introspection file ID and `mod.NameField`,
and hand the returned definition to `runModuleDefInSDK`, which patches it in
as today. Everything else in `runModuleDefInSDK` is untouched: the
`moduleTypes` branch and its `ErrStaleSDKCapability` fallback (`:3198` to
`:3210`), `IncludeSelfInDeps` (`:3246`), the eager-runtime construction with
the completed self schema and its sync (`:3256` to `:3290`), the per-client
patching. `Module.runtime` (`core/schema/module.go:2104`) and
`loadFunctionRuntime` (`core/modfunc.go:814`) see the same `mod.Runtime`
they see today. On a cache hit the capability decision and the runtime
selection are the same as on a miss, because they happen before the field
is selected.

What still runs per client: `moduleSource`, the dependency loads,
`_implementationScoped`, the runtime chain lookups (hits), `asModule`'s own
body, the legacy post-processing, `Module.serve`. That is the 0.4 s
measured.

## What can go wrong

- Stale definitions after a source change: a changed included file changes
  the context directory's digest and so the receiver's digest
  (`core/modulesource.go:1413`); a changed runtime identity or schema-file
  identity changes the call's arguments. Rebuilding the same deterministic
  runtime recipe need not miss. Different bytes produced under the same
  recipe are not detected, which is the ordinary cache-identity assumption
  and not stronger.
- `ErrStaleSDKCapability`: unchanged, it is decided before the field.
- SDKs with `moduleTypes`: unchanged, not routed through the field.
- A non-container runtime: unchanged, the uncached exec.
- Self calls (`:3247`): unchanged; on the container path the flag is not
  set, as today.
- Eager runtime (`:3256`): unchanged; `mod.Runtime` is set by the runtime
  load before the field, as today, so the guard behaves as today.
- References inside the definition: `TypeDef` results and their `SourceMap`
  results are the definition row's dependencies and relocate through export
  and import (`dagql/cache_value_capture.go:433`,
  `dagql/cache_value_import.go:256`), as today's typedef rows in A's `build`
  bundle did. The `Runtime` reference relocates like the module's today.
- Concurrent cold loads: the singleflight key includes the recipe digest and
  the session ID (`dagql/cache.go:5249`, `dagql/objects.go:612`), so two
  sessions, or two structurally equivalent recipes, need not join; both may
  run the exec, and the second result merges by identity afterwards. No
  change to singleflight is proposed.

Two facts from the Namespace track that bound the export of any retained
row, the definition included (coordinator, 17 September): a managed engine
is suspended by the API about a minute after its last client disconnects
and then receives SIGTERM with a 60-second grace, so A's export after
session end must start promptly, and it does, because the service queues
the export when it processes the report and the engine's open long-poll
returns at once (`engine/remotecache/client.go`, the poll loop); and a
SIGTERM in the middle of an export must leave nothing half-posted, and it
does not, because the bundle is posted last and a canceled upload fails
the export before P5 (`engine/remotecache/upload.go`). The demo API will
use a 15-minute window.

## How it is tested

- In-process, `core/schema`, with a scripted resolver counter: (1) two
  selections with equivalent inputs from two client contexts give one
  resolver run and one row; (2) a changed `moduleName` gives a second run;
  (3) a changed runtime argument alone, and a changed introspection file
  argument alone, each give a new run; (4) export through a module-object
  leaf, import into a second cache with different result numbers,
  reconstruct equivalent inputs there, and require a definition hit whose
  typedefs, `SourceMap`s and runtime reference equal the exported ones,
  which exercises the relocation path (`dagql/cache_value_import.go:256`).
- Engine test, `core/integration` (through `dagger api call engine-dev
  test`, one selection, bounded): a Go module loaded by two clients on one
  engine; one `module definition computed` line for the two loads, the
  second load's `asModule` span has no `asModule getModDef` child, the two
  clients' served definitions (objects, functions, source maps) are equal,
  and `Module.runtime` resolves on both; a source edit that adds a declared
  function produces a new definition with that function; and the retained
  branches keep their current behavior: a `moduleTypes` SDK with self calls,
  an eager-runtime client, and a stale-capability source.
- Cross-engine: the demo run by the service seat; the acceptance is direct
  evidence of the definition hit on B, the `_moduleDefinition` call
  `cached=true` in B's log and no `asModule getModDef` span, together with
  B's load time and the absence of the 225 MB `fs` download line. Time and
  download alone do not establish the hit.

## Export side

The definition row must reach B inside A's bundle. It is persistable, so it
has a retention edge on A, but as a leaf it would need its own export
command. The final module therefore keeps a reference to its definition:
`Module.Definition dagql.Nullable[dagql.ObjectResult[*Module]]`, wired in
all three places a module reference needs: `AttachDependencyResults`
(`core/module.go:840`), which establishes the closure edge; the persisted
encoding and decoding (`core/module.go:1022`, `:1107`) as
`DefinitionResultID` beside `RuntimeResultID`; and `persistedModuleVisitor`
(`core/persisted_visitors.go:502`), which relocates the ID on transfer.
With that edge the definition is inside the closure of every module-object
leaf and travels with `build`'s bundle; no policy change.

Cost: one more retained row per loaded module definition, metadata only in
itself, whose retention keeps its typedef and runtime dependencies alive and
whose closure's records go into every bundle that contains it
(`dagql/cache_value_capture.go:94`). Whether the runtime's parts are
uploaded is the policy's choice through `partsOf` and `OutputsOf`
(`engine/server/remote_cache.go:152`), not a property of this row. The
records are the same typedef and runtime rows the module-object leaf
already carries today, so the added bundle size is one row's record; not
measured. A definition that no leaf references stays a retained leaf and
exports on its own, metadata only.
