package dagql

import (
	"fmt"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
)

type cachePersistInputProvenanceKind string

const (
	cachePersistInputProvenanceKindResult cachePersistInputProvenanceKind = "result"
	cachePersistInputProvenanceKindDigest cachePersistInputProvenanceKind = "digest"
)

type cachePersistInputProvenance struct {
	Kind   cachePersistInputProvenanceKind `json:"kind"`
	Digest string                          `json:"digest"`
}

func (prov cachePersistInputProvenance) validate() error {
	if prov.Kind != cachePersistInputProvenanceKindResult && prov.Kind != cachePersistInputProvenanceKindDigest {
		return fmt.Errorf("unknown input provenance kind %q", prov.Kind)
	}
	if prov.Digest == "" {
		return fmt.Errorf("input provenance %q missing digest", prov.Kind)
	}
	return nil
}

type persistResultSnapshot struct {
	resultID              sharedResultID
	frame                 *ResultCall
	self                  Typed
	isObject              bool
	realized              bool
	sessionResourceHandle SessionResourceHandle
	persistedEnvelope     *PersistedResultEnvelope
	snapshotOwnerLinks    []PersistedSnapshotRefLink
	lazyFragment          *PersistedLazyFragment
	row                   persistdb.MirrorResult
	origin                persistdb.MirrorResultOrigin
	resultDeps            []persistdb.MirrorResultDep
	resultSnapshotLinks   []persistdb.MirrorResultSnapshotLink
}

type persistStateSnapshot struct {
	persistedEdges        []persistdb.MirrorPersistedEdge
	eqClasses             []persistdb.MirrorEqClass
	eqClassDigests        []persistdb.MirrorEqClassDigest
	terms                 []persistdb.MirrorTerm
	termInputs            []persistdb.MirrorTermInput
	resultOutputEqClasses []persistdb.MirrorResultOutputEqClass
	results               []persistResultSnapshot
	snapshotContentLinks  []persistdb.MirrorSnapshotContentLink
	importedLayerByBlob   []persistdb.MirrorImportedLayerBlobIndex
	importedLayerByDiff   []persistdb.MirrorImportedLayerDiffIndex

	// maxAllocatedResultID is the allocator high-water mark at snapshot
	// time, flushed into meta so boot resumes allocation above it.
	maxAllocatedResultID sharedResultID
}
