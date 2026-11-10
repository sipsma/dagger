package dagql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/dagger/dagger/engine/slog"
	"github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/snapshots/config"
	"github.com/opencontainers/go-digest"
)

// A bundle directory holds a value bundle as BundleFile and, under
// blobs/<algorithm>/<encoded> (ImageBlobPath), the blob of every layer its
// offers name. Its offers address each blob in the directory
// (ImageBlobAddress), never by a path: the form an engine image ships its
// entries in, which the engine merges at start (MergeValues from
// imageCacheID).

// BundleFile is the bundle's file in a bundle directory, beside its blobs.
const BundleFile = "bundle.json"

// BundleWriter receives a bundle as ExportBundle makes it: the bundle, then
// each blob its offers name, once each, by digest.
type BundleWriter interface {
	WriteBundle(context.Context, ValueBundle) error
	WriteBlob(ctx context.Context, d digest.Digest, r content.ReaderAt) error
}

// ExportBundle exports results as a bundle directory's content: each result,
// with its record and its closure's, and the complete parts of the result and
// of every result its value holds (its child references, followed
// recursively), whatever their types. A Git module source carries its
// checkout this way, and a container its filesystem, image config and exec
// metadata. Every result the export reads parts from is evaluated first, so
// its parts are complete. Every result must be captured. The engine it runs
// on may be busy with other work: a capture there, such as the remote
// cache's, can hold a row of the closure for a moment, and a capture never
// waits for a row (WithExportedValues). So when a result's closure reaches a
// row that's busy now, the export lets go of the capture, waits, and
// captures again, until every result is captured or ctx ends. A result
// refused for any other reason, or one whose row stays busy until a later
// evaluation (BusyBookkeeping), fails the export.
func (c *Cache) ExportBundle(ctx context.Context, results []AnyResult, cfg config.RefConfig, w BundleWriter) (rerr error) {
	if len(results) == 0 {
		return errors.New("export bundle: no results")
	}
	held, release, err := c.holdBundleRows(ctx, results)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, release(context.WithoutCancel(ctx))) }()

	var outputs []SelectedValueOutput
	for _, res := range held {
		if err := c.completeDeclaredParts(ctx, res); err != nil {
			return err
		}
		for _, key := range c.completePartKeys(ctx, res.cacheSharedResult()) {
			var address PersistedPartAddress
			if err := json.Unmarshal([]byte(key), &address); err != nil {
				return fmt.Errorf("export bundle: part %s: %w", key, err)
			}
			outputs = append(outputs, SelectedValueOutput{Result: res, Address: address})
		}
	}
	// Each given result is one root; what their values hold travels in
	// their closures.
	var roots []AnyResult
	seen := map[*sharedResult]bool{}
	for _, res := range results {
		shared := res.cacheSharedResult()
		if shared == nil || seen[shared] {
			continue
		}
		seen[shared] = true
		roots = append(roots, res)
	}
	selection := ValueSelection{Roots: roots, Outputs: outputs, EveryRoot: true}
	// Each wait is logged, and so is the capture after them, with the time
	// from the first busy capture to the one that captured every result:
	// the log is where this export meeting another capture shows.
	var waits int
	var waitStart time.Time
	for pause := bundleBusyPause; ; pause = min(2*pause, maxBundleBusyPause) {
		passStart := time.Now()
		err := c.WithExportedValues(ctx, selection, cfg, func(ctx context.Context, values *ExportedValues) error {
			return writeBundle(ctx, values, w)
		})
		left := leftOutOnly(err)
		if left == nil {
			if err == nil && waits > 0 {
				slog.Info("export bundle: every result captured after waiting for busy ones", "results", len(roots), "waits", waits, "waited", passStart.Sub(waitStart))
			}
			return err
		}
		first := left.Roots[0]
		err = fmt.Errorf("export bundle: %d of %d results left out, the first (row %d): %w", len(left.Roots), len(roots), first.Row, first.Err)
		if !busyForNow(left.Roots) {
			return err
		}
		if waits == 0 {
			waitStart = time.Now()
		}
		waits++
		slog.Info("export bundle: results busy, capturing again", "left_out", len(left.Roots), "results", len(roots), "row", first.Row, "kind", first.Kind, "error", first.Err, "pause", pause)
		select {
		case <-ctx.Done():
			return errors.Join(context.Cause(ctx), err)
		case <-time.After(pause):
		}
	}
}

// The export's first pause after a busy capture, and its longest: a busy
// row is mostly another capture's, held for well under a second.
const (
	bundleBusyPause    = 10 * time.Millisecond
	maxBundleBusyPause = time.Second
)

// leftOutOnly is the RootsLeftOutError err is when it is nothing more, such
// as joined with the capture's release error: nil otherwise.
func leftOutOnly(err error) *RootsLeftOutError {
	for {
		switch e := err.(type) {
		case *RootsLeftOutError:
			return e
		case interface{ Unwrap() []error }:
			errs := e.Unwrap()
			if len(errs) != 1 {
				return nil
			}
			err = errs[0]
		default:
			return nil
		}
	}
}

// busyForNow reports whether every root left out is busy with something
// that ends on its own: no busy row it reaches waits for a later
// evaluation.
func busyForNow(roots []LeftOutRoot) bool {
	for _, root := range roots {
		if !errors.Is(root.Err, ErrPersistStateNotReady) || root.Kind == BusyBookkeeping {
			return false
		}
		for _, busy := range root.Busy {
			if busy.Kind == BusyBookkeeping {
				return false
			}
		}
	}
	return true
}

// writeBundle writes a bundle from values, every root captured, and then
// each blob its outputs' chains name, once each.
func writeBundle(ctx context.Context, values *ExportedValues, w BundleWriter) error {
	bundle := values.Bundle
	address := func(addresses map[digest.Digest]BlobAddress, layers []snapshots.ExportLayer) map[digest.Digest]BlobAddress {
		if addresses == nil {
			addresses = map[digest.Digest]BlobAddress{}
		}
		for _, layer := range layers {
			addresses[layer.Descriptor.Digest] = ImageBlobAddress(layer.Descriptor.Digest)
		}
		return addresses
	}
	if len(bundle.Layers) > 0 {
		bundle.Addresses = address(bundle.Addresses, bundle.Layers)
	}
	for i := range bundle.Outputs {
		if chain := bundle.Outputs[i].Chain; chain != nil && len(chain.Layers) > 0 {
			chain.Addresses = address(chain.Addresses, chain.Layers)
		}
	}
	if err := w.WriteBundle(ctx, bundle); err != nil {
		return err
	}
	written := map[digest.Digest]bool{}
	for _, entry := range values.Chains.Entries {
		for _, layer := range entry.Layers {
			d := layer.Descriptor.Digest
			if written[d] {
				continue
			}
			written[d] = true
			if err := d.Validate(); err != nil {
				return err
			}
			r, err := entry.Provider.ReaderAt(ctx, layer.Descriptor)
			if err != nil {
				return fmt.Errorf("export bundle: blob %s: %w", d, err)
			}
			err = w.WriteBlob(ctx, d, r)
			if closeErr := r.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return fmt.Errorf("export bundle: blob %s: %w", d, err)
			}
		}
	}
	return nil
}

// holdBundleRows evaluates results and every result their values hold, their
// child references followed recursively, holding each one it reaches, and
// returns them: results first, then each held child once. A child is
// evaluated before its own references are read, since evaluating a value can
// complete what it holds.
func (c *Cache) holdBundleRows(ctx context.Context, results []AnyResult) (_ []AnyResult, _ func(context.Context) error, rerr error) {
	var releases []func(context.Context) error
	release := func(ctx context.Context) error {
		var err error
		for _, release := range releases {
			err = errors.Join(err, release(ctx))
		}
		return err
	}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, release(context.WithoutCancel(ctx)))
		}
	}()
	seen := map[sharedResultID]bool{}
	var held []AnyResult
	level := results
	for len(level) > 0 {
		if err := c.Evaluate(ctx, level...); err != nil {
			return nil, nil, fmt.Errorf("export bundle: evaluate: %w", err)
		}
		var next []AnyResult
		for _, res := range level {
			shared := res.cacheSharedResult()
			if shared == nil {
				return nil, nil, errors.New("export bundle: detached result")
			}
			if seen[shared.id] {
				continue
			}
			seen[shared.id] = true
			held = append(held, res)
			children, err := c.childReferences(ctx, shared)
			if err != nil {
				return nil, nil, fmt.Errorf("export bundle: result %d: %w", shared.id, err)
			}
			for _, id := range children {
				if seen[sharedResultID(id)] {
					continue
				}
				child, release, err := c.HoldEntry(ctx, id)
				if err != nil {
					return nil, nil, fmt.Errorf("export bundle: result %d's child %d: %w", shared.id, id, err)
				}
				releases = append(releases, release)
				next = append(next, child)
			}
		}
		level = next
	}
	return held, release, nil
}

// completeDeclaredParts evaluates the parts res's record declares and doesn't
// have complete yet, such as a container's filesystem when only its image
// config was read, so the export carries every part of the value.
func (c *Cache) completeDeclaredParts(ctx context.Context, res AnyResult) error {
	record, err := c.captureRowRecord(ctx, res.cacheSharedResult())
	if err != nil {
		return fmt.Errorf("export bundle: %w", err)
	}
	probes, err := describePartRecord(record)
	if err != nil {
		return fmt.Errorf("export bundle: %w", err)
	}
	var pending []PartKey
	for _, probe := range probes {
		if !probe.LocalComplete && len(probe.Descriptor.Address.OutputPath) == 0 && !slices.Contains(pending, probe.Descriptor.Address.Part) {
			pending = append(pending, probe.Descriptor.Address.Part)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	if err := c.EvaluateParts(ctx, res, pending...); err != nil {
		return fmt.Errorf("export bundle: evaluate parts %v: %w", pending, err)
	}
	return nil
}

// childReferences returns the results row's record holds as its children
// (PersistedRefChild): the results its value holds, as opposed to those its
// call only took as inputs.
func (c *Cache) childReferences(ctx context.Context, row *sharedResult) ([]uint64, error) {
	record, err := c.captureRowRecord(ctx, row)
	if err != nil {
		return nil, err
	}
	// An offer's owner references what made the offer, not what the value
	// holds.
	record.Envelope.PendingOffers = nil
	var ids []uint64
	if _, err := VisitEncodedReferences(record, func(ref *PersistedRef) error {
		if ref.Kind == PersistedRefChild && ref.RecipeID == nil && ref.ResultID != 0 && ref.ResultID != uint64(row.id) {
			ids = append(ids, ref.ResultID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return ids, nil
}
