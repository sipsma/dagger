package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	bkcache "github.com/dagger/dagger/engine/snapshots"
	bkclient "github.com/dagger/dagger/internal/buildkit/client"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/dagger/dagger/dagql"
)

type RemoteGitMirror struct {
	RemoteURL string

	mu               sync.Mutex
	snapshot         bkcache.MutableRef
	snapshotID       string
	externalSnapshot dagql.PersistedSnapshotRefLink
}

var _ dagql.PersistedObject = (*RemoteGitMirror)(nil)
var _ dagql.PersistedObjectDecoder = (*RemoteGitMirror)(nil)
var _ dagql.OnReleaser = (*RemoteGitMirror)(nil)

func NewRemoteGitMirror(remoteURL string) *RemoteGitMirror {
	return &RemoteGitMirror{RemoteURL: remoteURL}
}

func (*RemoteGitMirror) Type() *ast.Type {
	return &ast.Type{
		NamedType: "RemoteGitMirror",
		NonNull:   true,
	}
}

func (*RemoteGitMirror) TypeDescription() string {
	return "An internal persistent bare git mirror."
}

func (mirror *RemoteGitMirror) OnRelease(ctx context.Context) error {
	if mirror == nil {
		return nil
	}
	mirror.mu.Lock()
	defer mirror.mu.Unlock()
	if mirror.snapshot == nil {
		return nil
	}
	err := mirror.snapshot.Release(ctx)
	mirror.snapshot = nil
	return err
}

func (mirror *RemoteGitMirror) PersistedSnapshotRefLinks() []dagql.PersistedSnapshotRefLink {
	if mirror == nil {
		return nil
	}
	mirror.mu.Lock()
	defer mirror.mu.Unlock()
	if mirror.snapshot == nil && mirror.snapshotID == "" && !mirror.externalSnapshot.IsExternal() {
		return nil
	}
	if mirror.snapshot != nil {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: mirror.snapshot.SnapshotID(),
			Role:   "bare_repo",
		}}
	}
	if mirror.snapshotID != "" {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: mirror.snapshotID,
			Role:   "bare_repo",
		}}
	}
	return []dagql.PersistedSnapshotRefLink{{
		RefKey:   mirror.externalSnapshot.RefKey,
		Role:     "bare_repo",
		SourceID: mirror.externalSnapshot.SourceID,
	}}
}

func (mirror *RemoteGitMirror) CacheUsageMayChange() bool {
	return true
}

func (mirror *RemoteGitMirror) CacheUsageIdentities() []string {
	if mirror == nil {
		return nil
	}
	mirror.mu.Lock()
	defer mirror.mu.Unlock()
	if mirror.snapshot == nil && mirror.snapshotID == "" && !mirror.externalSnapshot.IsExternal() {
		return nil
	}
	if mirror.snapshot != nil {
		return []string{mirror.snapshot.SnapshotID()}
	}
	if mirror.snapshotID != "" {
		return []string{mirror.snapshotID}
	}
	return []string{mirror.externalSnapshot.RefKey}
}

func (mirror *RemoteGitMirror) cacheUsageSnapshotIDLocked() (string, bool) {
	if mirror.snapshot != nil {
		return mirror.snapshot.SnapshotID(), true
	}
	if mirror.snapshotID != "" {
		return mirror.snapshotID, true
	}
	if mirror.externalSnapshot.IsExternal() {
		return mirror.externalSnapshot.RefKey, false
	}
	return "", false
}

func (mirror *RemoteGitMirror) CacheUsageSize(ctx context.Context, identity string) (int64, bool, error) {
	if mirror == nil {
		return 0, false, nil
	}
	mirror.mu.Lock()
	snapshot := mirror.snapshot
	snapshotID, local := mirror.cacheUsageSnapshotIDLocked()
	mirror.mu.Unlock()
	if snapshotID == "" || snapshotID != identity {
		return 0, false, nil
	}
	if snapshot != nil {
		size, err := snapshot.Size(ctx)
		if err != nil {
			return 0, false, err
		}
		return size, true, nil
	}
	if !local {
		return 0, false, nil
	}
	query, err := CurrentQuery(ctx)
	if err != nil {
		return 0, false, err
	}
	size, err := query.SnapshotManager().SnapshotSize(ctx, snapshotID)
	if err != nil {
		return 0, false, err
	}
	return size, true, nil
}

func (mirror *RemoteGitMirror) persistedSnapshotLinksLocked() []dagql.PersistedSnapshotRefLink {
	if mirror.snapshot != nil {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: mirror.snapshot.SnapshotID(),
			Role:   "bare_repo",
		}}
	}
	if mirror.snapshotID != "" {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: mirror.snapshotID,
			Role:   "bare_repo",
		}}
	}
	if mirror.externalSnapshot.IsExternal() {
		return []dagql.PersistedSnapshotRefLink{mirror.externalSnapshot}
	}
	return nil
}

type persistedRemoteGitMirrorPayload struct {
	RemoteURL string `json:"remoteURL"`
}

func (mirror *RemoteGitMirror) EncodePersistedObject(ctx context.Context, cache dagql.PersistedObjectCache) (dagql.PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	if mirror == nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("encode persisted remote git mirror: nil mirror")
	}
	mirror.mu.Lock()
	links := mirror.persistedSnapshotLinksLocked()
	mirror.mu.Unlock()
	payload, err := json.Marshal(persistedRemoteGitMirrorPayload{
		RemoteURL: mirror.RemoteURL,
	})
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	return dagql.PersistedObjectEncoding{
		JSON:          payload,
		SnapshotLinks: links,
	}, nil
}

func (*RemoteGitMirror) DecodePersistedObject(ctx context.Context, dag *dagql.Server, resultID uint64, _ *dagql.ResultCall, payload json.RawMessage) (dagql.Typed, error) {
	var persisted persistedRemoteGitMirrorPayload
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode persisted remote git mirror payload: %w", err)
	}
	mirror := NewRemoteGitMirror(persisted.RemoteURL)
	if resultID == 0 {
		return mirror, nil
	}
	link, err := loadPersistedSnapshotLinkByResultID(ctx, dag, resultID, "remote git mirror", "bare_repo")
	if err != nil {
		return nil, err
	}
	if link.IsExternal() {
		mirror.externalSnapshot = link
	} else {
		mirror.snapshotID = link.RefKey
	}
	return mirror, nil
}

func (mirror *RemoteGitMirror) EnsureCreated(ctx context.Context, query *Query) error {
	if mirror == nil {
		return fmt.Errorf("remote git mirror is nil")
	}
	mirror.mu.Lock()
	defer mirror.mu.Unlock()
	return mirror.ensureSnapshotLocked(ctx, query)
}

func (mirror *RemoteGitMirror) acquire(ctx context.Context, query *Query) (_ bkcache.MutableRef, release func(), err error) {
	if mirror == nil {
		return nil, nil, fmt.Errorf("remote git mirror is nil")
	}
	mirror.mu.Lock()
	if err := mirror.ensureSnapshotLocked(ctx, query); err != nil {
		mirror.mu.Unlock()
		return nil, nil, err
	}
	return mirror.snapshot, mirror.mu.Unlock, nil
}

func (mirror *RemoteGitMirror) ensureSnapshotLocked(ctx context.Context, query *Query) error {
	if mirror.snapshot != nil {
		return nil
	}
	if mirror.snapshotID != "" {
		ref, err := query.SnapshotManager().GetMutableBySnapshotID(
			ctx,
			mirror.snapshotID,
			bkcache.NoUpdateLastUsed,
			bkcache.WithRecordType(bkclient.UsageRecordTypeGitCheckout),
			bkcache.WithDescription(fmt.Sprintf("git bare repo for %s", mirror.RemoteURL)),
		)
		if err != nil {
			return fmt.Errorf("reopen persisted remote git mirror snapshot %q: %w", mirror.snapshotID, err)
		}
		mirror.snapshot = ref
		return nil
	}
	if mirror.externalSnapshot.IsExternal() {
		cache, err := dagql.EngineCache(ctx)
		if err != nil {
			return err
		}
		base, err := cache.HydrateSnapshot(ctx, mirror.externalSnapshot)
		if err != nil {
			return fmt.Errorf("hydrate remote git mirror snapshot %q from %q: %w", mirror.externalSnapshot.RefKey, mirror.externalSnapshot.SourceID, err)
		}
		defer func() {
			_ = base.Release(context.WithoutCancel(ctx))
		}()
		ref, err := query.SnapshotManager().New(
			ctx,
			base,
			nil,
			bkcache.WithRecordType(bkclient.UsageRecordTypeGitCheckout),
			bkcache.WithDescription(fmt.Sprintf("git bare repo for %s", mirror.RemoteURL)),
		)
		if err != nil {
			return fmt.Errorf("initialize remote git mirror from persisted snapshot %q: %w", mirror.externalSnapshot.RefKey, err)
		}
		mirror.snapshot = ref
		mirror.snapshotID = ref.SnapshotID()
		mirror.externalSnapshot = dagql.PersistedSnapshotRefLink{}
		return nil
	}
	ref, err := query.SnapshotManager().New(
		ctx,
		nil,
		nil,
		bkcache.WithRecordType(bkclient.UsageRecordTypeGitCheckout),
		bkcache.WithDescription(fmt.Sprintf("git bare repo for %s", mirror.RemoteURL)),
	)
	if err != nil {
		return err
	}
	mirror.snapshot = ref
	return nil
}
