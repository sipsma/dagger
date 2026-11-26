package snapshots

import (
	"context"
	stderrors "errors"
	"path"
	"slices"

	"github.com/dagger/dagger/engine/snapshots/config"
)

// fileChainKey names a File's own-content chain (ExportFileChain) of one
// snapshot: the file's path, and what of the export's config makes its
// blobs.
type fileChainKey struct {
	path        string
	compression string
	level       int
	force       bool
	listings    bool
	nonDist     bool
}

func newFileChainKey(filePath string, refCfg config.RefConfig) fileChainKey {
	key := fileChainKey{
		path:     path.Clean("/" + filePath),
		force:    refCfg.Compression.Force,
		listings: refCfg.Listings,
		nonDist:  refCfg.PreferNonDistributable,
		level:    -1,
	}
	if refCfg.Compression.Type != nil {
		key.compression = refCfg.Compression.Type.String()
	}
	if refCfg.Compression.Level != nil {
		key.level = *refCfg.Compression.Level
	}
	return key
}

// reuseFileChain returns the chain an earlier export of the file built,
// pinned for the caller, when its blobs are all still in the store: a
// File's chain is built once per snapshot and path, as a snapshot's own
// layer is.
func (cm *snapshotManager) reuseFileChain(ctx context.Context, snapshotID string, key fileChainKey) (_ *ExportChain, ok bool, rerr error) {
	cm.mu.Lock()
	layers := cm.fileChains[snapshotID][key]
	cm.mu.Unlock()
	if layers == nil {
		return nil, false, nil
	}
	pin, pinCtx, err := cm.newResourcePin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if !ok {
			rerr = stderrors.Join(rerr, pin.release(pinCtx))
		}
	}()
	chain := &ExportChain{
		Provider: cm.ContentStore,
		Prefixes: map[string]int{},
		Layers:   make([]ExportLayer, 0, len(layers)),
		pin:      pin,
	}
	for _, layer := range layers {
		present, err := cm.pinContent(pinCtx, layer.Descriptor)
		if err != nil || !present {
			return nil, false, err
		}
		// Built by an earlier export, not this one.
		layer.Computed = false
		chain.Layers = append(chain.Layers, layer)
		if IsContent(layer.Descriptor) {
			chain.Stats.Contents++
			continue
		}
		chain.Stats.Layers++
		chain.Stats.LayersReused++
		if IsListing(layer.Descriptor) {
			chain.Stats.Listings++
		}
	}
	return chain, true, nil
}

// recordFileChain keeps a File's chain as the snapshot's: each blob is
// recorded as content of the snapshot, held as long as it is owned, and
// the chain is what a later export of the file takes (reuseFileChain).
func (cm *snapshotManager) recordFileChain(ctx context.Context, snapshotID string, key fileChainKey, layers []ExportLayer) error {
	for _, layer := range layers {
		if err := cm.recordSnapshotContent(ctx, snapshotID, layer.Descriptor); err != nil {
			return err
		}
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.fileChains == nil {
		cm.fileChains = map[string]map[fileChainKey][]ExportLayer{}
	}
	if cm.fileChains[snapshotID] == nil {
		cm.fileChains[snapshotID] = map[fileChainKey][]ExportLayer{}
	}
	cm.fileChains[snapshotID][key] = slices.Clone(layers)
	return nil
}
