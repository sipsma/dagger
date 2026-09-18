# Foreign local-path audit (report appendix)

Operational uses now call LocalContextDirectoryPath before using the recorded
host path: loadContextFromSource, LoadContextFile, LoadContextGit, innerEnvFile,
ResolveDepToSource's local host fallback, ModuleSourceFS.Stat/Exists,
ModTreeNode.buildScaleOutModuleQuery (source and separate context), local config
conversion in moduleConfigDependencyForRelatedSource (both operands), toolchain
context conversion, and pendingRelatedModule (related and default-path context).
Their existing Workspace alternatives remain first where available. Optional
Git/env/Exists paths preserve the sentinel; it is not an absence error.

The remaining reads are data-only:
- Constructors store the native path, and the checked accessor returns it.
- ModuleSource.AsString, the path schema field, module descriptive Ref,
  ModTree.RootAddress and diagnostics/telemetry format recorded data.
- withSourceSubpath validates lexical containment, then reloads via the existing
  Workspace route or the guarded host route.
- Related-source validation, deduplication, update and removal compare paths and
  retain flagged objects through clones.
- resolveDefaultPathContextSource hashes a descriptive reference when there is
  no context tree; it retains the original source object.
- canonicalModuleReference and isSameModuleReference compare recorded data.
- resolveModuleSourceAsModule fills diagnostic mod.Ref while selecting asModule
  on the retained source object; it does not construct a fresh source selection.
- engine/server/telemetry.go AsString calls are OTel attribute reads.

Every matching production use from the core and engine/server search follows.
Tests and generated testdata are excluded. This inventory is part of REPORT.md.

```text
core/modtree.go:595:		localPath, err := modSrc.LocalContextDirectoryPath()
core/modtree.go:606:			Arg("refString", modSrc.AsString()).
core/modtree.go:633:				if _, err := contextSrc.LocalContextDirectoryPath(); err != nil {
core/modtree.go:637:			contextRef := contextSrc.AsString()
core/modtree.go:638:			if contextRef != "" && (contextRef != modSrc.AsString() || contextSrc.Pin() != modSrc.Pin()) {
core/modtree.go:708:	return modSrc.AsString()
engine/server/telemetry.go:101:			return attr.Value.AsString()
engine/server/telemetry.go:111:			origin = attr.Value.AsString()
core/module.go:2347:		ref = filepath.Join(src.Local.ContextDirectoryPath, src.SourceRootSubpath)
engine/server/session.go:2786:				mod.Name(), mod.GetSource().AsString(), mod.GetSource().Pin(), existing.GetSource().AsString(), existing.GetSource().Pin(),
engine/server/session.go:2803:	if a.AsString() == "" || b.AsString() == "" {
engine/server/session_workspaces.go:111:		return filepath.Clean(filepath.Join(src.Local.ContextDirectoryPath, sourceSubpath))
engine/server/session_workspaces.go:116:		return src.AsString()
engine/server/session_workspaces.go:2266:		if _, err := related.LocalContextDirectoryPath(); err != nil {
engine/server/session_workspaces.go:2271:		if _, err := contextSource.LocalContextDirectoryPath(); err != nil {
engine/server/session_workspaces.go:2277:		Ref:        related.AsString(),
engine/server/session_workspaces.go:2288:		mod.DefaultPathContextSourceRef = defaultPathContextSrc.Self().AsString()
engine/server/session_workspaces.go:2299:		mod.LegacyCallerModuleDir = defaultPathContextSrc.Self().AsString()
engine/server/session_workspaces.go:2311:		mod.Ref = src.Self().AsString()
core/modulesource.go:1032:		return filepath.Join(src.Local.ContextDirectoryPath, src.SourceRootSubpath)
core/modulesource.go:1117:		ref = src.AsString()
core/modulesource.go:1128:	ref := src.AsString()
core/modulesource.go:1177:	localPath, err := src.LocalContextDirectoryPath()
core/modulesource.go:1641:		ctxPath, err := src.LocalContextDirectoryPath()
core/modulesource.go:1839:		ctxPath, err := src.LocalContextDirectoryPath()
core/modulesource.go:1942:		localPath, err = src.LocalContextDirectoryPath()
core/modulesource.go:2051:	ContextDirectoryPath string
core/modulesource.go:2227:			parentPath, err := parentSrc.LocalContextDirectoryPath()
core/modulesource.go:2557:		localPath, err := fs.src.LocalContextDirectoryPath()
core/modulesource.go:2591:		localPath, err := fs.src.LocalContextDirectoryPath()
core/foreign_module_context.go:10:// LocalContextDirectoryPath is the checked host-path accessor. Formatting and
core/foreign_module_context.go:12:func (src *ModuleSource) LocalContextDirectoryPath() (string, error) {
core/foreign_module_context.go:17:		return "", fmt.Errorf("%w: module %q at %q", ErrForeignModuleContext, src.ModuleName, src.AsString())
core/foreign_module_context.go:19:	return src.Local.ContextDirectoryPath, nil
core/telemetry.go:343:				calleeRef.ref = strings.ReplaceAll(calleeRef.ref, ms.Local.ContextDirectoryPath, "")
core/schema/modulesource.go:240:		dagql.Func("localContextDirectoryPath", s.moduleSourceLocalContextDirectoryPath).
core/schema/modulesource.go:662:			ContextDirectoryPath: contextDirPath,
core/schema/modulesource.go:728:				return "", false, fmt.Errorf("git module source %q does not contain a dagger config file", gitSrc.AsString())
core/schema/modulesource.go:753:			return "", false, fmt.Errorf("git module source %q does not contain a dagger config file", gitSrc.AsString())
core/schema/modulesource.go:983:		src.Local = &core.LocalModuleSource{ContextDirectoryPath: base.HostPath}
core/schema/modulesource.go:1297:		contextRootPath = src.Local.ContextDirectoryPath
core/schema/modulesource.go:1335:	return src.AsString(), nil
core/schema/modulesource.go:1592:func (s *moduleSourceSchema) moduleSourceLocalContextDirectoryPath(
core/schema/modulesource.go:1600:	return src.Local.ContextDirectoryPath, nil
core/schema/modulesource.go:1646:					parentSrc.Local.ContextDirectoryPath,
core/schema/modulesource.go:1647:					newRelatedModule.Self().Local.ContextDirectoryPath,
core/schema/modulesource.go:1654:						accessor.typ, newRelatedModule.Self().Local.ContextDirectoryPath, parentSrc.Local.ContextDirectoryPath)
core/schema/modulesource.go:1707:			symbolicItemStr = filepath.Join(item.Self().Local.ContextDirectoryPath, item.Self().SourceRootSubpath)
core/schema/modulesource.go:1822:					contextRoot = parentSrc.Self().Local.ContextDirectoryPath
core/schema/modulesource.go:1931:			parentSrcRoot := filepath.Join(parentSrc.Local.ContextDirectoryPath, parentSrc.SourceRootSubpath)
core/schema/modulesource.go:1932:			itemSrcRoot := filepath.Join(parentSrc.Local.ContextDirectoryPath, existingItem.Self().SourceRootSubpath)
core/schema/modulesource.go:2071:			parentPath, err := parentSrc.LocalContextDirectoryPath()
core/schema/modulesource.go:2075:			relatedPath, err := relatedSrc.LocalContextDirectoryPath()
core/schema/modulesource.go:2148:	return src.AsString()
core/schema/modulesource.go:3521:				defaultPathContextSrc.Self().AsString(),
core/schema/modulesource.go:3831:					if _, err := defaultPathContextSrc.Self().LocalContextDirectoryPath(); err != nil {
core/schema/modulesource.go:3835:				dpRef = defaultPathContextSrc.Self().AsString()
```
