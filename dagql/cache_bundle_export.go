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
// with its record and its closure's, and the complete parts of the result,
// whatever its type. A result with no parts of its own carries those of the
// results its value holds instead (its child references), and so on down
// through any that have none either. A container carries its filesystem,
// image config and exec metadata, which are its own parts, and a Git module
// source, which has none, carries its checkout's. What a result with parts
// was made from travels as records only: a lookup of the result reads its own
// parts, not those of what made it. Every result the export reads parts from is decoded, with
// resolver, and evaluated first, with every part it declares, and a part that
// still isn't complete fails the export. Every result must be captured. The
// engine it runs on may be busy with other work: a capture there, such as
// the remote cache's, can hold a row of the closure for a moment, and a
// capture never waits for a row (WithExportedValues). So when a result's
// closure reaches a row that's busy now, the export lets go of the capture,
// waits, and captures again, until every result is captured or ctx ends.
// Its setup's own reads of the results' records wait the same way. A result refused for any other
// reason, or one whose row stays busy until a later evaluation
// (BusyBookkeeping), fails the export at once.
func (c *Cache) ExportBundle(ctx context.Context, resolver TypeResolver, results []AnyResult, cfg config.RefConfig, w BundleWriter) (rerr error) {
	if len(results) == 0 {
		return errors.New("export bundle: no results")
	}
	builder := CacheID(c.Identity().ID)
	if builder == "" {
		// Its rows' runs are named by this cache's identity (writeBundle):
		// without one, two bundles' runs could share a name.
		return errors.New("export bundle: the cache has no identity to name its runs by")
	}
	// Every read of a busy row, the setup's and the capture's, shares one
	// backoff and one count of the pauses (bundleWait).
	wait := &bundleWait{}
	held, release, err := c.holdBundleRows(ctx, resolver, results, wait)
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, release(context.WithoutCancel(ctx))) }()

	var outputs []SelectedValueOutput
	for _, res := range held {
		if err := c.completeDeclaredParts(ctx, res, wait); err != nil {
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
	for {
		err := c.WithExportedValues(ctx, selection, cfg, func(ctx context.Context, values *ExportedValues) error {
			return writeBundle(ctx, builder, values, w)
		})
		left := leftOutOnly(err)
		if left == nil {
			if err == nil && wait.waits > 0 {
				slog.Info("export bundle: every result captured after waiting for busy rows", "results", len(roots), "waits", wait.waits, "waited", wait.waited)
			}
			return err
		}
		first := left.Roots[0]
		err = fmt.Errorf("export bundle: %d of %d results left out, the first (row %d): %w", len(left.Roots), len(roots), first.Row, first.Err)
		if !busyForNow(left.Roots) {
			return err
		}
		if err := wait.wait(ctx, err, "left_out", len(left.Roots), "results", len(roots), "row", first.Row, "kind", first.Kind); err != nil {
			return err
		}
	}
}

// bundleWait is an export's wait for busy rows, in its setup's reads and in
// its capture. Each pause is logged, and the export's log of its capture
// after them gives their total: the log is where this export meeting
// another capture shows.
type bundleWait struct {
	pause  time.Duration
	waits  int
	waited time.Duration
}

// wait pauses before the export tries again a read that err, a busy row's
// error, refused. It returns nil once it has paused, or, when ctx ends,
// the error to fail the export with: err with ctx's cause. attrs describe
// the busy read, for the log.
func (w *bundleWait) wait(ctx context.Context, err error, attrs ...any) error {
	if w.waits == 0 {
		w.pause = bundleBusyPause
	}
	w.waits++
	slog.Info("export bundle: rows busy, reading again", append(attrs, "error", err, "pause", w.pause)...)
	select {
	case <-ctx.Done():
		return errors.Join(context.Cause(ctx), err)
	case <-time.After(w.pause):
	}
	w.waited += w.pause
	w.pause = min(2*w.pause, maxBundleBusyPause)
	return nil
}

// readRowRecord is captureRowRecord for the export's setup, which waits, as
// its capture does, for a row that's busy now, unless only a later
// evaluation would free it.
func (c *Cache) readRowRecord(ctx context.Context, row *sharedResult, wait *bundleWait) (PersistedRecord, error) {
	for {
		record, err := c.captureRowRecord(ctx, row)
		if err == nil || !busyOnly(err) {
			return record, err
		}
		kind := row.busyKind()
		if kind == BusyBookkeeping {
			return record, err
		}
		if err := wait.wait(ctx, err, "row", row.id, "kind", kind); err != nil {
			return record, err
		}
	}
}

// busyOnly reports whether err says only that a row is busy now: not also
// another error joined with it, such as a failed release, which must not be
// retried away.
func busyOnly(err error) bool {
	for {
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok {
			return errors.Is(err, ErrPersistStateNotReady)
		}
		errs := joined.Unwrap()
		if len(errs) != 1 {
			return false
		}
		err = errs[0]
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
func writeBundle(ctx context.Context, builder CacheID, values *ExportedValues, w BundleWriter) error {
	bundle := values.Bundle
	bundle.Values = slices.Clone(bundle.Values)
	for i := range bundle.Values {
		// Every engine merges a bundle directory's rows from one sender,
		// imageCacheID, so a row's sender number alone would name the same
		// run in every bundle built. Each row names its run by the cache that
		// made it: builder, or the one a merge gave the entry.
		if bundle.Values[i].Run == nil {
			run := TransferredRun(builder, bundle.Values[i])
			bundle.Values[i].Run = &run
		}
	}
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

// holdBundleRows evaluates results and, for each one with no parts of its
// own, the results its value holds, followed down through those that have
// none either, holding each one it reaches, and returns them: results first,
// then each held child once. A result is evaluated before its parts and
// references are read, since evaluating a value can complete what it holds.
func (c *Cache) holdBundleRows(ctx context.Context, resolver TypeResolver, results []AnyResult, wait *bundleWait) (_ []AnyResult, _ func(context.Context) error, rerr error) {
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
			children, err := c.partHolders(ctx, shared, wait)
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
				// A held entry is undecoded, and an undecoded value has
				// nothing to evaluate its parts with.
				decoded, err := c.ensurePersistedHitValueLoaded(ctx, resolver, child)
				if err != nil {
					return nil, nil, fmt.Errorf("export bundle: result %d's child %d: %w", shared.id, id, err)
				}
				next = append(next, decoded)
			}
		}
		level = next
	}
	return held, release, nil
}

// completeDeclaredParts evaluates the parts res's record declares and doesn't
// have complete yet, such as a container's filesystem when only its image
// config was read, so the export carries every part of the value. It fails
// when one is still not complete after.
func (c *Cache) completeDeclaredParts(ctx context.Context, res AnyResult, wait *bundleWait) error {
	pending, err := c.pendingDeclaredParts(ctx, res, wait)
	if err != nil || len(pending) == 0 {
		return err
	}
	if err := c.EvaluateParts(ctx, res, pending...); err != nil {
		return fmt.Errorf("export bundle: result %d: evaluate parts %v: %w", res.cacheSharedResult().id, pending, err)
	}
	if pending, err = c.pendingDeclaredParts(ctx, res, wait); err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("export bundle: result %d: parts %v are not complete after evaluating them", res.cacheSharedResult().id, pending)
	}
	return nil
}

// pendingDeclaredParts returns the parts at the root of res's value that its
// record declares and doesn't have complete.
func (c *Cache) pendingDeclaredParts(ctx context.Context, res AnyResult, wait *bundleWait) ([]PartKey, error) {
	record, err := c.readRowRecord(ctx, res.cacheSharedResult(), wait)
	if err != nil {
		return nil, fmt.Errorf("export bundle: %w", err)
	}
	probes, err := describePartRecord(record)
	if err != nil {
		return nil, fmt.Errorf("export bundle: %w", err)
	}
	var pending []PartKey
	for _, probe := range probes {
		if !probe.LocalComplete && len(probe.Descriptor.Address.OutputPath) == 0 && !slices.Contains(pending, probe.Descriptor.Address.Part) {
			pending = append(pending, probe.Descriptor.Address.Part)
		}
	}
	return pending, nil
}

// partHolders returns, for a row whose record declares no parts of its own,
// the results its record holds as its children (PersistedRefChild): the
// results its value holds, as opposed to those its call only took as inputs.
// A row with parts of its own returns none.
func (c *Cache) partHolders(ctx context.Context, row *sharedResult, wait *bundleWait) ([]uint64, error) {
	record, err := c.readRowRecord(ctx, row, wait)
	if err != nil {
		return nil, err
	}
	probes, err := describePartRecord(record)
	if err != nil {
		return nil, err
	}
	if len(probes) > 0 {
		return nil, nil
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
