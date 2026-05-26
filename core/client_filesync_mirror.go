package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/dagger/dagger/engine/filesync"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	bkclient "github.com/dagger/dagger/internal/buildkit/client"
	"github.com/dagger/dagger/internal/buildkit/identity"
	digest "github.com/opencontainers/go-digest"
	"github.com/vektah/gqlparser/v2/ast"
	"google.golang.org/grpc"

	"github.com/dagger/dagger/dagql"
)

type ClientFilesyncMirror struct {
	StableClientID string
	Drive          string
	EphemeralID    string

	mu sync.Mutex

	snapshot         bkcache.MutableRef
	snapshotID       string
	externalSnapshot dagql.PersistedSnapshotRefLink

	mounter bkcache.Mounter
	mntPath string

	sharedState *filesync.MirrorSharedState
	usageCount  int
}

var _ dagql.PersistedObject = (*ClientFilesyncMirror)(nil)
var _ dagql.PersistedObjectDecoder = (*ClientFilesyncMirror)(nil)
var _ dagql.OnReleaser = (*ClientFilesyncMirror)(nil)

func (*ClientFilesyncMirror) Type() *ast.Type {
	return &ast.Type{
		NamedType: "ClientFilesyncMirror",
		NonNull:   true,
	}
}

func (*ClientFilesyncMirror) TypeDescription() string {
	return "An internal persistent filesync mirror."
}

func (m *ClientFilesyncMirror) PersistedSnapshotRefLinks() []dagql.PersistedSnapshotRefLink {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshot != nil {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: m.snapshot.SnapshotID(),
			Role:   "snapshot",
		}}
	}
	if m.snapshotID != "" {
		return []dagql.PersistedSnapshotRefLink{{
			RefKey: m.snapshotID,
			Role:   "snapshot",
		}}
	}
	if m.externalSnapshot.IsExternal() {
		return []dagql.PersistedSnapshotRefLink{m.externalSnapshot}
	}
	return nil
}

func (m *ClientFilesyncMirror) cacheUsageSnapshotIDLocked() (string, bool) {
	if m.snapshot != nil {
		return m.snapshot.SnapshotID(), true
	}
	if m.snapshotID != "" {
		return m.snapshotID, true
	}
	if m.externalSnapshot.IsExternal() {
		return m.externalSnapshot.RefKey, false
	}
	return "", false
}

func (m *ClientFilesyncMirror) CacheUsageMayChange() bool {
	return true
}

func (m *ClientFilesyncMirror) CacheUsageIdentities() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	snapshotID, _ := m.cacheUsageSnapshotIDLocked()
	m.mu.Unlock()
	if snapshotID == "" {
		return nil
	}
	return []string{snapshotID}
}

func (m *ClientFilesyncMirror) CacheUsageSize(ctx context.Context, identity string) (int64, bool, error) {
	if m == nil {
		return 0, false, nil
	}
	m.mu.Lock()
	snapshot := m.snapshot
	snapshotID, local := m.cacheUsageSnapshotIDLocked()
	m.mu.Unlock()
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

type persistedClientFilesyncMirrorPayload struct {
	StableClientID string `json:"stableClientID"`
	Drive          string `json:"drive,omitempty"`
}

func (m *ClientFilesyncMirror) EncodePersistedObject(ctx context.Context, cache dagql.PersistedObjectCache) (dagql.PersistedObjectEncoding, error) {
	_ = ctx
	_ = cache
	if m == nil {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("encode persisted client filesync mirror: nil mirror")
	}
	if m.StableClientID == "" {
		return dagql.PersistedObjectEncoding{}, fmt.Errorf("encode persisted client filesync mirror: stable client id is empty")
	}
	m.mu.Lock()
	var links []dagql.PersistedSnapshotRefLink
	switch {
	case m.snapshot != nil:
		links = []dagql.PersistedSnapshotRefLink{{
			RefKey: m.snapshot.SnapshotID(),
			Role:   "snapshot",
		}}
	case m.snapshotID != "":
		links = []dagql.PersistedSnapshotRefLink{{
			RefKey: m.snapshotID,
			Role:   "snapshot",
		}}
	case m.externalSnapshot.IsExternal():
		links = []dagql.PersistedSnapshotRefLink{m.externalSnapshot}
	}
	m.mu.Unlock()
	payload, err := json.Marshal(persistedClientFilesyncMirrorPayload{
		StableClientID: m.StableClientID,
		Drive:          m.Drive,
	})
	if err != nil {
		return dagql.PersistedObjectEncoding{}, err
	}
	return dagql.PersistedObjectEncoding{
		JSON:          payload,
		SnapshotLinks: links,
	}, nil
}

func (*ClientFilesyncMirror) DecodePersistedObject(ctx context.Context, dag *dagql.Server, resultID uint64, _ *dagql.ResultCall, payload json.RawMessage) (dagql.Typed, error) {
	var persisted persistedClientFilesyncMirrorPayload
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode persisted client filesync mirror payload: %w", err)
	}
	mirror := &ClientFilesyncMirror{
		StableClientID: persisted.StableClientID,
		Drive:          persisted.Drive,
	}
	if resultID == 0 {
		return mirror, nil
	}

	link, err := loadPersistedSnapshotLinkByResultID(ctx, dag, resultID, "client filesync mirror", "snapshot")
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

func (m *ClientFilesyncMirror) OnRelease(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usageCount = 0
	rerr := m.releaseRuntimeLocked()
	if m.snapshot != nil {
		rerr = errorsJoin(rerr, m.snapshot.Release(ctx))
		m.snapshot = nil
	}
	return rerr
}

func (m *ClientFilesyncMirror) EnsureCreated(ctx context.Context, query *Query) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snapshot != nil {
		return nil
	}
	if m.snapshotID != "" {
		ref, err := query.SnapshotManager().GetMutableBySnapshotID(ctx, m.snapshotID, bkcache.NoUpdateLastUsed)
		if err != nil {
			return fmt.Errorf("reopen persisted client filesync mirror snapshot %q: %w", m.snapshotID, err)
		}
		m.snapshot = ref
		return nil
	}
	if m.externalSnapshot.IsExternal() {
		cache, err := dagql.EngineCache(ctx)
		if err != nil {
			return err
		}
		base, err := cache.HydrateSnapshot(ctx, m.externalSnapshot)
		if err != nil {
			return fmt.Errorf("hydrate client filesync mirror snapshot %q from %q: %w", m.externalSnapshot.RefKey, m.externalSnapshot.SourceID, err)
		}
		defer func() {
			_ = base.Release(context.WithoutCancel(ctx))
		}()
		ref, err := query.SnapshotManager().New(
			ctx,
			base,
			nil,
			bkcache.WithRecordType(bkclient.UsageRecordTypeLocalSource),
			bkcache.WithDescription(func() string {
				if m.StableClientID != "" {
					return fmt.Sprintf("client filesync mirror for %s%s", m.Drive, m.StableClientID)
				}
				return fmt.Sprintf("ephemeral client filesync mirror for %s%s", m.Drive, m.EphemeralID)
			}()),
		)
		if err != nil {
			return fmt.Errorf("initialize client filesync mirror from persisted snapshot %q: %w", m.externalSnapshot.RefKey, err)
		}
		m.snapshot = ref
		m.snapshotID = ref.SnapshotID()
		m.externalSnapshot = dagql.PersistedSnapshotRefLink{}
		return nil
	}
	ref, err := query.SnapshotManager().New(
		ctx,
		nil,
		nil,
		bkcache.WithRecordType(bkclient.UsageRecordTypeLocalSource),
		bkcache.WithDescription(func() string {
			if m.StableClientID != "" {
				return fmt.Sprintf("client filesync mirror for %s%s", m.Drive, m.StableClientID)
			}
			return fmt.Sprintf("ephemeral client filesync mirror for %s%s", m.Drive, m.EphemeralID)
		}()),
	)
	if err != nil {
		return err
	}
	m.snapshot = ref
	return nil
}

func (m *ClientFilesyncMirror) Snapshot(
	ctx context.Context,
	query *Query,
	callerConn *grpc.ClientConn,
	clientPath string,
	opts filesync.SnapshotOpts,
) (bkcache.ImmutableRef, digest.Digest, error) {
	sharedState, release, err := m.acquire(ctx, query)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		_ = release(context.WithoutCancel(ctx))
	}()
	return filesync.NewFileSyncer(filesync.FileSyncerOpt{
		CacheAccessor: query.SnapshotManager(),
	}).Snapshot(ctx, sharedState, callerConn, clientPath, opts)
}

func (m *ClientFilesyncMirror) acquire(ctx context.Context, query *Query) (_ *filesync.MirrorSharedState, release func(context.Context) error, err error) {
	m.mu.Lock()
	if err := m.ensureRuntimeLocked(ctx, query); err != nil {
		m.mu.Unlock()
		return nil, nil, err
	}
	m.usageCount++
	sharedState := m.sharedState
	m.mu.Unlock()
	return sharedState, func(_ context.Context) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.usageCount--
		if m.usageCount > 0 {
			return nil
		}
		return m.releaseRuntimeLocked()
	}, nil
}

func (m *ClientFilesyncMirror) ensureRuntimeLocked(ctx context.Context, query *Query) error {
	if m.sharedState != nil {
		return nil
	}
	if m.snapshot == nil {
		ref, err := query.SnapshotManager().New(
			ctx,
			nil,
			nil,
			bkcache.WithRecordType(bkclient.UsageRecordTypeLocalSource),
			bkcache.WithDescription(func() string {
				if m.StableClientID != "" {
					return fmt.Sprintf("client filesync mirror for %s%s", m.Drive, m.StableClientID)
				}
				return fmt.Sprintf("ephemeral client filesync mirror for %s%s", m.Drive, m.EphemeralID)
			}()),
		)
		if err != nil {
			return err
		}
		m.snapshot = ref
	}

	mountable, err := m.snapshot.Mount(ctx, false)
	if err != nil {
		return err
	}
	m.mounter = bkcache.LocalMounter(mountable)
	m.mntPath, err = m.mounter.Mount()
	if err != nil {
		return err
	}
	m.sharedState = filesync.NewMirrorSharedState(m.mntPath)
	return nil
}

func (m *ClientFilesyncMirror) releaseRuntimeLocked() (rerr error) {
	if m.mounter != nil {
		rerr = errorsJoin(rerr, m.mounter.Unmount())
		m.mounter = nil
	}
	m.mntPath = ""
	m.sharedState = nil
	return rerr
}

func errorsJoin(errs ...error) error {
	var out error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if out == nil {
			out = err
		} else {
			out = fmt.Errorf("%w; %w", out, err)
		}
	}
	return out
}

func NewEphemeralClientFilesyncMirror(drive string) *ClientFilesyncMirror {
	return &ClientFilesyncMirror{
		Drive:       drive,
		EphemeralID: identity.NewID(),
	}
}
