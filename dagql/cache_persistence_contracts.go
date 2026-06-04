package dagql

import (
	"context"
	"fmt"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
	bkcache "github.com/dagger/dagger/engine/snapshots"
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
	hasValue              bool
	sessionResourceHandle SessionResourceHandle
	persistedEnvelope     *PersistedResultEnvelope
	snapshotOwnerLinks    []PersistedSnapshotRefLink
	row                   persistdb.MirrorResult
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
}

// PersistedSnapshotSource describes a durable source for snapshots referenced by
// imported cache metadata. Local cache bundles and remote cachemoney sources both
// implement this interface.
type PersistedSnapshotSource interface {
	HydrateSnapshot(context.Context, string, bkcache.SnapshotManager) (bkcache.ImmutableRef, error)
	AddSnapshotToBundle(context.Context, *bkcache.CacheBundleWriter, string) (bkcache.BundleSnapshot, error)
}

// PersistedCacheSource describes one imported metadata source. dagql keeps the
// source identity so imported metadata can preserve source-local references until
// materialized.
type PersistedCacheSource struct {
	ID             string
	Dir            string
	MetadataDBPath string
	Snapshots      PersistedSnapshotSource
}
