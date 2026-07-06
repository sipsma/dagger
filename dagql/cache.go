package dagql

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	telemetry "github.com/dagger/otel-go"
	"github.com/google/uuid"
	set "github.com/hashicorp/go-set/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	_ "modernc.org/sqlite"

	"github.com/dagger/dagger/dagql/call"
	persistdb "github.com/dagger/dagger/dagql/persistdb"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/slog"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/engine/telemetryattrs"
	"github.com/opencontainers/go-digest"
	"github.com/vektah/gqlparser/v2/ast"
)

func ValueFunc(v AnyResult) func(context.Context) (AnyResult, error) {
	return func(context.Context) (AnyResult, error) {
		return v, nil
	}
}

type CacheEntryStats struct {
	OngoingCalls            int
	CompletedCalls          int
	RetainedCalls           int
	CompletedCallsByContent int
	OngoingArbitrary        int
	CompletedArbitrary      int
}

type CacheUsageEntry struct {
	ID                        string
	Description               string
	RecordType                string
	RecordTypes               []string
	DagqlCall                 string
	SizeBytes                 int64
	CreatedTimeUnixNano       int64
	MostRecentUseTimeUnixNano int64
	ActivelyUsed              bool
}

type CachePrunePolicy struct {
	All           bool
	Filters       []string
	KeepDuration  time.Duration
	ReservedSpace int64
	MaxUsedSpace  int64
	MinFreeSpace  int64
	TargetSpace   int64

	// CurrentFreeSpace is optional available-disk bytes at prune start used to
	// evaluate MinFreeSpace. When unset, MinFreeSpace behaves as if free space
	// were zero.
	CurrentFreeSpace int64
}

type CachePruneReport struct {
	Entries        []CacheUsageEntry
	ReclaimedBytes int64
}

type persistedEdge struct {
	resultID          sharedResultID
	createdAtUnixNano int64
	expiresAtUnix     int64
	unpruneable       bool
}

const cachePersistenceSchemaVersion = "19"

var ErrCacheRecursiveCall = fmt.Errorf("recursive call detected")

// errSourcesExhausted reports that a restored result's retained sources are
// permanently exhausted: nothing remains that could deliver its value. It is
// consumed inside the lookup path, where the hit demotes to a miss; callers
// never see it.
var errSourcesExhausted = errors.New("cached result's retained sources are exhausted")

// errSourcesUnavailable reports a walk that could not deliver right now but
// might later: a transient failure (network transport, snapshotter apply)
// starved every remaining source. The caller demotes to a miss exactly like
// exhaustion — the run computes honestly (S4) — but the row is NOT dropped:
// nothing permanent was learned about its sources, so the next lookup's
// walk retries them (reset §9 D2's transient rule). Like the exhaustion
// sentinel, it never escapes the cache.
var errSourcesUnavailable = errors.New("cached result's retained sources are transiently unavailable")

// isTransientMaterializeFailure classifies retained-source walk failures.
// The rule is deliberately dumb: cancellation and deadline are transient —
// the next demander retries the walk — and everything else the work itself
// produced is permanent.
func isTransientMaterializeFailure(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

var ErrPersistStateNotReady = errors.New("persist state not ready")

type CachePersistenceResetReason string

const (
	CachePersistenceResetNone            CachePersistenceResetReason = ""
	CachePersistenceResetSchemaMismatch  CachePersistenceResetReason = "schema_mismatch"
	CachePersistenceResetUncleanShutdown CachePersistenceResetReason = "unclean_shutdown"
	CachePersistenceResetImportFailure   CachePersistenceResetReason = "import_failure"
)

func NewCache(
	ctx context.Context,
	dbPath string,
	snapshotManager bkcache.SnapshotManager,
	snapshotGC func(context.Context) error,
) (*Cache, error) {
	c := &Cache{
		traceBootID:     newTraceBootID(),
		snapshotManager: snapshotManager,
		snapshotGC:      snapshotGC,
	}

	if dbPath == "" {
		return c, nil
	}

	c.statsFilePath = cacheStatsFilePath(dbPath)

	db, persistDB, err := prepareCacheDBs(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	c.sqlDB = db
	c.pdb = persistDB

	schemaVersionVal, found, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeySchemaVersion)
	if err != nil {
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("read schema_version metadata: %w", err), closeErr)
		}
		return nil, fmt.Errorf("read schema_version metadata: %w", err)
	}
	if found && schemaVersionVal != cachePersistenceSchemaVersion {
		c.persistenceResetReason = CachePersistenceResetSchemaMismatch
		c.restoreSummary = &CacheRestoreSummary{Wiped: true, Reason: string(CachePersistenceResetSchemaMismatch)}
		c.tracePersistStoreWipedSchemaMismatch(ctx, cachePersistenceSchemaVersion, schemaVersionVal)
		slog.Warn("dagql persistence store schema version mismatch; wiping and cold-starting", "expected", cachePersistenceSchemaVersion, "actual", schemaVersionVal)
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("close db before schema-version wipe"), closeErr)
		}
		if err := wipeSQLiteFiles(dbPath); err != nil {
			return nil, fmt.Errorf("wipe schema-mismatched persistence db: %w", err)
		}
		if err := c.sweepAllDaggerOwnerLeases(ctx); err != nil {
			return nil, err
		}

		db, persistDB, err = prepareCacheDBs(ctx, dbPath)
		if err != nil {
			return nil, err
		}
		c.sqlDB = db
		c.pdb = persistDB
	}

	cleanShutdownVal, found, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeyCleanShutdown)
	if err != nil {
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("read clean_shutdown metadata: %w", err), closeErr)
		}
		return nil, fmt.Errorf("read clean_shutdown metadata: %w", err)
	}
	if found && cleanShutdownVal != "1" {
		c.persistenceResetReason = CachePersistenceResetUncleanShutdown
		c.restoreSummary = &CacheRestoreSummary{Wiped: true, Reason: string(CachePersistenceResetUncleanShutdown)}
		c.tracePersistStoreWipedUncleanShutdown(ctx, cleanShutdownVal)
		slog.Warn("dagql persistence store marked unclean; wiping and cold-starting", "cleanShutdown", cleanShutdownVal)
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("close db before wipe"), closeErr)
		}
		if err := wipeSQLiteFiles(dbPath); err != nil {
			return nil, fmt.Errorf("wipe unclean persistence db: %w", err)
		}
		if err := c.sweepAllDaggerOwnerLeases(ctx); err != nil {
			return nil, err
		}

		db, persistDB, err = prepareCacheDBs(ctx, dbPath)
		if err != nil {
			return nil, err
		}
		c.sqlDB = db
		c.pdb = persistDB
	}
	if err := c.loadStoreIdentity(ctx); err != nil {
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}
	if err := c.importPersistedState(ctx); err != nil {
		c.persistenceResetReason = CachePersistenceResetImportFailure
		c.restoreSummary = &CacheRestoreSummary{Wiped: true, Reason: string(CachePersistenceResetImportFailure)}
		c.tracePersistStoreWipedImportFailure(ctx, err)
		slog.Warn("dagql persistence import failed; wiping and cold-starting", "err", err)
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("close db before import-wipe"), closeErr)
		}
		if err := wipeSQLiteFiles(dbPath); err != nil {
			return nil, fmt.Errorf("wipe persistence db after import failure: %w", err)
		}
		if err := c.sweepAllDaggerOwnerLeases(ctx); err != nil {
			return nil, err
		}
		db, persistDB, err = prepareCacheDBs(ctx, dbPath)
		if err != nil {
			return nil, err
		}
		c.sqlDB = db
		c.pdb = persistDB
		// The wipe destroyed the store's meta, and with it the store's
		// identity: a fresh UUID retires every origin the old store ever
		// minted (they age out of any remote inventory on their own).
		if err := c.loadStoreIdentity(ctx); err != nil {
			if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
			return nil, err
		}
	}

	if err := c.pdb.UpsertMeta(ctx, persistdb.MetaKeySchemaVersion, cachePersistenceSchemaVersion); err != nil {
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("set persistence schema version: %w", err), closeErr)
		}
		return nil, fmt.Errorf("set persistence schema version: %w", err)
	}
	if err := c.pdb.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "0"); err != nil {
		if closeErr := closeCacheDBs(db, c.pdb); closeErr != nil {
			return nil, errors.Join(fmt.Errorf("mark clean_shutdown=0 at startup: %w", err), closeErr)
		}
		return nil, fmt.Errorf("mark clean_shutdown=0 at startup: %w", err)
	}
	return c, nil
}

// loadStoreIdentity loads (or mints, for a fresh store) the store UUID and
// the allocator high-water mark from meta. It runs after every wipe
// decision in NewCache, so a wiped store always starts with a fresh
// identity — which retires every origin the old store minted.
func (c *Cache) loadStoreIdentity(ctx context.Context) error {
	storeUUID, found, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeyStoreUUID)
	if err != nil {
		return fmt.Errorf("read store_uuid metadata: %w", err)
	}
	if !found || storeUUID == "" {
		storeUUID = uuid.NewString()
		if err := c.pdb.UpsertMeta(ctx, persistdb.MetaKeyStoreUUID, storeUUID); err != nil {
			return fmt.Errorf("mint store_uuid metadata: %w", err)
		}
	}
	c.storeUUID = storeUUID

	highWaterVal, found, err := c.pdb.SelectMetaValue(ctx, persistdb.MetaKeyMaxResultID)
	if err != nil {
		return fmt.Errorf("read max_result_id metadata: %w", err)
	}
	if found && highWaterVal != "" {
		highWater, err := strconv.ParseUint(highWaterVal, 10, 64)
		if err != nil {
			return fmt.Errorf("parse max_result_id metadata %q: %w", highWaterVal, err)
		}
		c.noteAllocatedResultIDLocked(sharedResultID(highWater))
	}
	return nil
}

// noteAllocatedResultIDLocked advances the allocator high-water mark; called
// wherever a result ID is assigned or observed. The mark is monotone for the
// store's lifetime: it survives in-memory e-graph drain resets and restores
// from meta at boot, so an ID that ever named a result — including one an
// export already published as an origin — is never allocated again.
func (c *Cache) noteAllocatedResultIDLocked(id sharedResultID) {
	if id > c.maxAllocatedResultID {
		c.maxAllocatedResultID = id
	}
}

// assignResultOriginLocked binds a result's durable origin pair and indexes
// it. First assignment wins: a result that already carries an origin (a
// bundle-imported row) keeps it.
func (c *Cache) assignResultOriginLocked(res *sharedResult, origin resultOrigin) {
	if res == nil || !res.origin.isZero() || origin.isZero() {
		return
	}
	res.origin = origin
	if c.resultsByOrigin == nil {
		c.resultsByOrigin = make(map[resultOrigin]sharedResultID)
	}
	c.resultsByOrigin[origin] = res.id
}

func (c *Cache) trackSessionResult(ctx context.Context, sessionID string, res AnyResult, hitCache bool) {
	if c == nil || sessionID == "" || res == nil {
		return
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return
	}

	acquired := false
	trackedCount := 0
	c.sessionMu.Lock()
	if c.sessionResultIDsBySession == nil {
		c.sessionResultIDsBySession = make(map[string]map[sharedResultID]struct{})
	}
	if c.sessionResultIDsBySession[sessionID] == nil {
		c.sessionResultIDsBySession[sessionID] = make(map[sharedResultID]struct{})
	}
	if _, found := c.sessionResultIDsBySession[sessionID][shared.id]; !found {
		c.sessionResultIDsBySession[sessionID][shared.id] = struct{}{}
		acquired = true
	}
	trackedCount = len(c.sessionResultIDsBySession[sessionID])
	c.sessionMu.Unlock()

	if acquired {
		c.egraphMu.Lock()
		if c.resultsByID[shared.id] == shared {
			c.incrementIncomingOwnershipLocked(ctx, shared)
		}
		c.egraphMu.Unlock()
	}

	if c.traceEnabled() {
		c.traceSessionResultTracked(ctx, sessionID, res, hitCache, trackedCount)
	}
}

func (c *Cache) recomputeRequiredSessionResourcesLocked(res *sharedResult) error {
	if res == nil {
		return nil
	}

	var reqs *set.TreeSet[SessionResourceHandle]
	if res.sessionResourceHandle != "" {
		reqs = set.NewTreeSet(compareSessionResourceHandles)
		reqs.Insert(res.sessionResourceHandle)
	}
	for depID := range res.deps {
		dep := c.resultsByID[depID]
		if dep == nil {
			return fmt.Errorf("recompute required session resources: missing dep result %d", depID)
		}
		if dep.requiredSessionResources == nil {
			continue
		}
		if reqs == nil {
			reqs = dep.requiredSessionResources.Copy()
		} else {
			reqs = reqs.Union(dep.requiredSessionResources).(*set.TreeSet[SessionResourceHandle])
		}
	}
	if reqs == nil || reqs.Empty() {
		res.requiredSessionResources = nil
		return nil
	}
	res.requiredSessionResources = reqs
	return nil
}

func (c *Cache) BindSessionResource(_ context.Context, sessionID string, clientID string, handle SessionResourceHandle, value any) error {
	if c == nil {
		return errors.New("bind session resource: nil cache")
	}
	if sessionID == "" {
		return errors.New("bind session resource: empty session ID")
	}
	if clientID == "" {
		return errors.New("bind session resource: empty client ID")
	}
	if handle == "" {
		return errors.New("bind session resource: empty handle")
	}
	if value == nil {
		return errors.New("bind session resource: nil concrete value")
	}

	c.sessionMu.Lock()
	if c.sessionResourcesBySession == nil {
		c.sessionResourcesBySession = make(map[string]map[SessionResourceHandle]*sessionResourceBindings)
	}
	if c.sessionResourcesBySession[sessionID] == nil {
		c.sessionResourcesBySession[sessionID] = make(map[SessionResourceHandle]*sessionResourceBindings)
	}
	sessionBindings := c.sessionResourcesBySession[sessionID]
	bindings := sessionBindings[handle]
	if bindings == nil {
		bindings = &sessionResourceBindings{
			byClientID: make(map[string]any),
		}
		sessionBindings[handle] = bindings
	}
	bindings.byClientID[clientID] = value
	bindings.latestClientID = clientID
	if c.sessionHandlesBySession == nil {
		c.sessionHandlesBySession = make(map[string]*set.TreeSet[SessionResourceHandle])
	}
	if c.sessionHandlesBySession[sessionID] == nil {
		c.sessionHandlesBySession[sessionID] = set.NewTreeSet(compareSessionResourceHandles)
	}
	c.sessionHandlesBySession[sessionID].Insert(handle)
	c.sessionMu.Unlock()

	return nil
}

func (c *Cache) SetVolatileVars(_ context.Context, sessionID, k, v string) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()

	if c.sessionVolatileVarsBySession == nil {
		c.sessionVolatileVarsBySession = make(map[string]map[string]string)
	}
	if c.sessionVolatileVarsBySession[sessionID] == nil {
		c.sessionVolatileVarsBySession[sessionID] = make(map[string]string)
	}
	c.sessionVolatileVarsBySession[sessionID][k] = v
}

func (c *Cache) ResolveVolatileVars(_ context.Context, sessionID string) map[string]string {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()

	if c.sessionVolatileVarsBySession == nil {
		return nil
	}
	if c.sessionVolatileVarsBySession[sessionID] == nil {
		return nil
	}
	return maps.Clone(c.sessionVolatileVarsBySession[sessionID])
}

func (c *Cache) ResolveSessionResource(
	ctx context.Context,
	sessionID string,
	clientID string,
	handle SessionResourceHandle,
) (any, error) {
	candidates, err := c.ResolveSessionResourceCandidates(ctx, sessionID, clientID, handle)
	if err != nil {
		return nil, err
	}
	return candidates[0].Value, nil
}

func (c *Cache) ResolveSessionResourceCandidates(
	_ context.Context,
	sessionID string,
	clientID string,
	handle SessionResourceHandle,
) ([]SessionResourceCandidate, error) {
	if c == nil {
		return nil, errors.New("resolve session resource: nil cache")
	}
	if sessionID == "" {
		return nil, errors.New("resolve session resource: empty session ID")
	}
	if clientID == "" {
		return nil, errors.New("resolve session resource: empty client ID")
	}
	if handle == "" {
		return nil, errors.New("resolve session resource: empty handle")
	}

	c.sessionMu.Lock()
	sessionBindings := c.sessionResourcesBySession[sessionID]
	bindings := sessionBindings[handle]
	if bindings == nil || len(bindings.byClientID) == 0 {
		c.sessionMu.Unlock()
		return nil, fmt.Errorf("resolve session resource %q: no bound resource for session %q", handle, sessionID)
	}

	candidates := make([]SessionResourceCandidate, 0, len(bindings.byClientID))
	seen := make(map[string]struct{}, len(bindings.byClientID))
	appendCandidate := func(candidateClientID string) {
		if candidateClientID == "" {
			return
		}
		if _, ok := seen[candidateClientID]; ok {
			return
		}
		value, ok := bindings.byClientID[candidateClientID]
		if !ok {
			return
		}
		seen[candidateClientID] = struct{}{}
		candidates = append(candidates, SessionResourceCandidate{
			ClientID: candidateClientID,
			Value:    value,
		})
	}

	appendCandidate(clientID)
	appendCandidate(bindings.latestClientID)

	otherClientIDs := make([]string, 0, len(bindings.byClientID))
	for candidateClientID := range bindings.byClientID {
		if _, ok := seen[candidateClientID]; ok {
			continue
		}
		otherClientIDs = append(otherClientIDs, candidateClientID)
	}
	slices.Sort(otherClientIDs)
	for _, candidateClientID := range otherClientIDs {
		appendCandidate(candidateClientID)
	}
	c.sessionMu.Unlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("resolve session resource %q: no binding for client %q in session %q", handle, clientID, sessionID)
	}
	return candidates, nil
}

func (c *Cache) captureSessionLazySpanContext(ctx context.Context, sessionID string, res AnyResult) {
	if c == nil || sessionID == "" || res == nil {
		return
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return
	}
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return
	}

	c.sessionMu.Lock()
	if c.sessionLazySpansBySession == nil {
		c.sessionLazySpansBySession = make(map[string]map[sharedResultID]trace.SpanContext)
	}
	if c.sessionLazySpansBySession[sessionID] == nil {
		c.sessionLazySpansBySession[sessionID] = make(map[sharedResultID]trace.SpanContext)
	}
	if _, exists := c.sessionLazySpansBySession[sessionID][shared.id]; !exists {
		c.sessionLazySpansBySession[sessionID][shared.id] = spanCtx
	}
	c.sessionMu.Unlock()
}

func (c *Cache) sessionLazySpanContext(sessionID string, resultID sharedResultID) (trace.SpanContext, bool) {
	if c == nil || sessionID == "" || resultID == 0 {
		return trace.SpanContext{}, false
	}

	c.sessionMu.Lock()
	spanCtx := c.sessionLazySpansBySession[sessionID][resultID]
	c.sessionMu.Unlock()
	if !spanCtx.IsValid() {
		return trace.SpanContext{}, false
	}
	return spanCtx, true
}

// captureSessionResultInstallSpan records the current span context as an
// install site for res in the given session. This wires explicit provenance
// for lazy failure attribution: the resume span of a later-failing lazy value
// looks up its install spans here and adds them as cause links.
//
// Trivial fields (auto-generated unwrap accessors) skip capture so they don't
// claim ownership of values they merely return.
func (c *Cache) captureSessionResultInstallSpan(ctx context.Context, sessionID string, res AnyResult) {
	if c == nil || sessionID == "" || res == nil {
		return
	}
	if CurrentFieldIsTrivial(ctx) {
		return
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return
	}
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return
	}

	// Module API call returns own their result's entire transitive dep
	// closure: module-returned values are typically constructed by inner SDK
	// calls whose own install spans aren't visible to the user, so failures
	// anywhere in the construction chain attribute back to the module API
	// span. Closure ownership is recorded once on the returned result and
	// resolved on demand by walking dep edges upward from the result being
	// evaluated (installAncestorIDsLocked), rather than eagerly fanning the
	// span out across every result in the closure.
	call := CurrentCall(ctx)
	ownsClosure := call != nil && call.Module != nil
	c.recordSessionResultInstallSpanLocked(sessionID, shared.id, spanCtx, ownsClosure)
}

// sessionResultInstallSpan is one recorded install site for a result in a
// session: the span context of the API call that returned/owns the result,
// plus whether that ownership extends over the result's transitive dep
// closure (module API call returns).
type sessionResultInstallSpan struct {
	spanCtx     trace.SpanContext
	ownsClosure bool
}

func (c *Cache) recordSessionResultInstallSpanLocked(sessionID string, resultID sharedResultID, spanCtx trace.SpanContext, ownsClosure bool) {
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.sessionResultInstallSpans == nil {
		c.sessionResultInstallSpans = make(map[string]map[sharedResultID]map[string]sessionResultInstallSpan)
	}
	bySession := c.sessionResultInstallSpans[sessionID]
	if bySession == nil {
		bySession = make(map[sharedResultID]map[string]sessionResultInstallSpan)
		c.sessionResultInstallSpans[sessionID] = bySession
	}
	byResult := bySession[resultID]
	if byResult == nil {
		byResult = make(map[string]sessionResultInstallSpan)
		bySession[resultID] = byResult
	}
	key := spanContextKey(spanCtx)
	install := byResult[key]
	install.spanCtx = spanCtx
	install.ownsClosure = install.ownsClosure || ownsClosure
	byResult[key] = install
}

// installAncestorIDsLocked returns the IDs of results that transitively
// depend on rootID, found by walking direct dep edges upward via depParents.
// rootID itself is excluded. Caller must hold egraphMu at least for read.
func (c *Cache) installAncestorIDsLocked(rootID sharedResultID) []sharedResultID {
	if rootID == 0 {
		return nil
	}
	root := c.resultsByID[rootID]
	if root == nil || root.depParents == nil || root.depParents.Empty() {
		return nil
	}
	seen := map[sharedResultID]struct{}{rootID: {}}
	queue := []sharedResultID{rootID}
	var out []sharedResultID
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		res := c.resultsByID[id]
		if res == nil || res.depParents == nil {
			continue
		}
		for parentID := range res.depParents.Items() {
			if _, ok := seen[parentID]; ok {
				continue
			}
			seen[parentID] = struct{}{}
			out = append(out, parentID)
			queue = append(queue, parentID)
		}
	}
	return out
}

// ResultInstallSpans returns install span contexts recorded for res in the
// given session — i.e. the API spans whose call returned (or owns) this
// result. Used to attribute later runtime failures (e.g. a service exiting
// early) back to the API span that installed the value.
func (c *Cache) ResultInstallSpans(sessionID string, res AnyResult) []trace.SpanContext {
	if c == nil || sessionID == "" || res == nil {
		return nil
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return nil
	}
	return c.sessionResultInstallSpanContexts(sessionID, shared.id)
}

// sessionResultInstallSpanContexts returns the install span contexts for
// resultID in the given session: the spans recorded directly for the result,
// plus closure-owning install spans (module API call returns) recorded for
// any result that transitively depends on it. The upward walk happens here,
// on demand, instead of eagerly materializing the closure at install time.
func (c *Cache) sessionResultInstallSpanContexts(sessionID string, resultID sharedResultID) []trace.SpanContext {
	if c == nil || sessionID == "" || resultID == 0 {
		return nil
	}

	c.egraphMu.RLock()
	ancestorIDs := c.installAncestorIDsLocked(resultID)
	c.egraphMu.RUnlock()

	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	byResult := c.sessionResultInstallSpans[sessionID]
	if byResult == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []trace.SpanContext
	appendInstalls := func(id sharedResultID, closureOwnersOnly bool) {
		for key, install := range byResult[id] {
			if closureOwnersOnly && !install.ownsClosure {
				continue
			}
			if !install.spanCtx.IsValid() {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, install.spanCtx)
		}
	}
	appendInstalls(resultID, false)
	for _, ancestorID := range ancestorIDs {
		appendInstalls(ancestorID, true)
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, compareSpanContexts)
	return out
}

func lazyResumeLinks(originalSpanCtx trace.SpanContext, installSpanContexts []trace.SpanContext) []trace.Link {
	links := []trace.Link{{SpanContext: originalSpanCtx}}
	seen := map[string]struct{}{spanContextKey(originalSpanCtx): {}}
	for _, installCtx := range installSpanContexts {
		if !installCtx.IsValid() {
			continue
		}
		key := spanContextKey(installCtx)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		links = append(links, trace.Link{
			SpanContext: installCtx,
			Attributes: []attribute.KeyValue{
				attribute.String(telemetry.LinkPurposeAttr, telemetry.LinkPurposeCause),
			},
		})
	}
	return links
}

func HasPendingLazyEvaluation(res AnyResult) bool {
	if res == nil {
		return false
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return false
	}

	shared.materializeMu.Lock()
	defer shared.materializeMu.Unlock()
	if shared.lazyEvalComplete {
		return false
	}
	if shared.lazyEval != nil {
		return true
	}
	return lazyEvalFuncOfResult(res) != nil
}

func (c *Cache) trackSessionArbitrary(sessionID string, res ArbitraryCachedResult) {
	if c == nil || sessionID == "" || res == nil {
		return
	}
	shared, ok := res.(arbitraryResult)
	if !ok || shared.shared == nil {
		return
	}

	acquired := false
	c.sessionMu.Lock()
	if c.sessionArbitraryCallKeysBySession == nil {
		c.sessionArbitraryCallKeysBySession = make(map[string]map[string]struct{})
	}
	if c.sessionArbitraryCallKeysBySession[sessionID] == nil {
		c.sessionArbitraryCallKeysBySession[sessionID] = make(map[string]struct{})
	}
	if _, found := c.sessionArbitraryCallKeysBySession[sessionID][shared.shared.callKey]; !found {
		c.sessionArbitraryCallKeysBySession[sessionID][shared.shared.callKey] = struct{}{}
		acquired = true
	}
	c.sessionMu.Unlock()

	if acquired {
		c.callsMu.Lock()
		shared.shared.ownerSessionCount++
		c.callsMu.Unlock()
	}
}

func (c *Cache) ReleaseSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("release session: empty session ID")
	}
	if c == nil {
		return nil
	}

	c.sessionMu.Lock()
	resultIDs := c.sessionResultIDsBySession[sessionID]
	arbitraryCallKeys := c.sessionArbitraryCallKeysBySession[sessionID]
	delete(c.sessionResultIDsBySession, sessionID)
	delete(c.sessionArbitraryCallKeysBySession, sessionID)
	delete(c.sessionLazySpansBySession, sessionID)
	delete(c.sessionResultInstallSpans, sessionID)
	delete(c.sessionResourcesBySession, sessionID)
	delete(c.sessionVolatileVarsBySession, sessionID)
	delete(c.sessionHandlesBySession, sessionID)
	c.sessionMu.Unlock()

	var (
		rerr       error
		onReleases []OnReleaseFunc
	)
	c.egraphMu.Lock()
	queue := make([]*sharedResult, 0, len(resultIDs))
	for resultID := range resultIDs {
		shared := c.resultsByID[resultID]
		if shared == nil {
			continue
		}
		res := Result[Typed]{shared: shared}
		if c.traceEnabled() {
			c.traceSessionResultReleasing(ctx, sessionID, res, "release_session", 1, len(resultIDs))
		}
		var err error
		queue, err = c.decrementIncomingOwnershipLocked(ctx, shared, queue)
		rerr = errors.Join(rerr, err)
	}
	collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
	onReleases = append(onReleases, collectReleases...)
	rerr = errors.Join(rerr, collectErr)
	c.egraphMu.Unlock()

	rerr = errors.Join(rerr, runOnReleaseFuncs(context.WithoutCancel(ctx), onReleases))
	for callKey := range arbitraryCallKeys {
		var onRelease OnReleaseFunc
		c.callsMu.Lock()
		res := c.completedArbitraryCalls[callKey]
		if res == nil {
			res = c.ongoingArbitraryCalls[callKey]
		}
		if res != nil {
			res.ownerSessionCount--
			if res.ownerSessionCount < 0 {
				res.ownerSessionCount = 0
			}
			if res.ownerSessionCount == 0 && res.waiters == 0 {
				if existing := c.ongoingArbitraryCalls[callKey]; existing == res {
					delete(c.ongoingArbitraryCalls, callKey)
				}
				if existing := c.completedArbitraryCalls[callKey]; existing == res {
					delete(c.completedArbitraryCalls, callKey)
				}
				onRelease = res.onRelease
			}
		}
		c.callsMu.Unlock()
		if onRelease != nil {
			rerr = errors.Join(rerr, onRelease(context.WithoutCancel(ctx)))
		}
	}
	return rerr
}

func (c *Cache) snapshotSessionResultIDs() map[sharedResultID]struct{} {
	if c == nil {
		return nil
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if len(c.sessionResultIDsBySession) == 0 {
		return nil
	}
	roots := make(map[sharedResultID]struct{})
	for _, resultIDs := range c.sessionResultIDsBySession {
		for resultID := range resultIDs {
			roots[resultID] = struct{}{}
		}
	}
	return roots
}

func (c *Cache) upsertPersistedEdgeLocked(ctx context.Context, res *sharedResult, expiresAtUnix int64, unpruneable bool) {
	if c == nil || res == nil || res.id == 0 {
		return
	}
	if c.persistedEdgesByResult == nil {
		c.persistedEdgesByResult = make(map[sharedResultID]persistedEdge)
	}
	edge, found := c.persistedEdgesByResult[res.id]
	if !found {
		createdAtUnixNano := res.loadPayloadState().createdAtUnixNano
		if createdAtUnixNano == 0 {
			createdAtUnixNano = time.Now().UnixNano()
		}
		edge = persistedEdge{
			resultID:          res.id,
			createdAtUnixNano: createdAtUnixNano,
		}
		c.incrementIncomingOwnershipLocked(ctx, res)
	}
	if unpruneable {
		edge.unpruneable = true
		edge.expiresAtUnix = 0
		res.expiresAtUnix = 0
	} else if !edge.unpruneable {
		edge.expiresAtUnix = mergeSharedResultExpiryUnix(edge.expiresAtUnix, expiresAtUnix)
	}
	c.persistedEdgesByResult[res.id] = edge
}

func (c *Cache) MakeResultUnpruneable(ctx context.Context, res AnyResult) error {
	if c == nil {
		return fmt.Errorf("make result unpruneable: nil cache")
	}
	if res == nil {
		return fmt.Errorf("make result unpruneable: nil result")
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return fmt.Errorf("make result unpruneable: result is not cache-backed")
	}

	c.egraphMu.Lock()
	c.upsertPersistedEdgeLocked(ctx, shared, 0, true)
	c.egraphMu.Unlock()
	return nil
}

// dropExhaustedResult removes a result whose retained sources are
// permanently exhausted — and, transitively, every result depending on it —
// from future servability: lookup candidacy, persisted retention, and
// snapshot owner leases. It never reaches into live holders: values already
// in hand stay usable, in-flight uses are unaffected, and each row's full
// cleanup runs through normal release once its holders let go. The same
// zero-viable-sources rule boot vetting applies, at its second moment.
func (c *Cache) dropExhaustedResult(ctx context.Context, res *sharedResult) error {
	if c == nil || res == nil || res.id == 0 {
		return nil
	}

	var (
		targets []*sharedResult
		queue   []*sharedResult
		rerr    error
	)
	droppedLinks := map[*sharedResult][]PersistedSnapshotRefLink{}
	c.egraphMu.Lock()
	seen := map[sharedResultID]struct{}{}
	pending := []*sharedResult{res}
	for len(pending) > 0 {
		target := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if target == nil {
			continue
		}
		if _, alreadySeen := seen[target.id]; alreadySeen {
			continue
		}
		seen[target.id] = struct{}{}
		if target.dropped {
			continue
		}
		targets = append(targets, target)
		if target.depParents != nil {
			for _, parentID := range target.depParents.Slice() {
				if parent := c.resultsByID[parentID]; parent != nil {
					pending = append(pending, parent)
				}
			}
		}
	}
	for _, target := range targets {
		target.dropped = true
		c.deindexResultCandidacyLocked(ctx, target)
		c.traceResultDroppedSourcesExhausted(ctx, target)
		if _, found := c.persistedEdgesByResult[target.id]; found {
			delete(c.persistedEdgesByResult, target.id)
			q, err := c.decrementIncomingOwnershipLocked(ctx, target, nil)
			queue = append(queue, q...)
			rerr = errors.Join(rerr, err)
		}
		// Deindexing removes the row from recipe lookups, but result-ID
		// handles still reach it directly. An undecoded dropped row must
		// refuse those touches through the exhaustion machinery, not attempt
		// a decode its sources can no longer back — so the envelope and the
		// retained sources go too, leaving the state with nothing to deliver
		// (servable() false). A realized target keeps its value: holders in
		// flight stay usable. The links come out with the sources here; the
		// lease removal below works from this capture.
		target.payloadMu.Lock()
		if !target.materialization.realized {
			target.materialization.envelope = nil
		}
		droppedLinks[target] = target.materialization.localSnapshotLinks()
		target.materialization.sources = nil
		target.payloadMu.Unlock()
	}
	collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
	rerr = errors.Join(rerr, collectErr)
	c.egraphMu.Unlock()

	// Dropped rows must not pin content: their owner leases go now. Values
	// still held keep their own open refs; the content becomes collectible
	// once those release.
	if c.snapshotManager != nil {
		for _, target := range targets {
			links := droppedLinks[target]
			seenLeases := make(map[string]struct{}, len(links))
			for _, link := range links {
				leaseID := resultSnapshotLeaseID(target.id, link.Role)
				if _, alreadySeen := seenLeases[leaseID]; alreadySeen {
					continue
				}
				seenLeases[leaseID] = struct{}{}
				rerr = errors.Join(rerr, c.snapshotManager.RemoveLease(ctx, leaseID))
			}
		}
	}
	return errors.Join(rerr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases))
}

// normalizeExhaustedResultError consumes retained-source exhaustion at
// surfaces that have no invocation to re-execute (result attachment, wait's
// post-completion normalization, handle loads). The rule mirrors the demote
// floor one level down: the exhausted row drops — with its dependents — so
// future lookups miss and heal, and this use receives an honest error. The
// sentinel itself never escapes the cache; no caller may observe or match
// it. Dropping is idempotent, so surfaces downstream of a runner that
// already dropped are safe to normalize again.
func (c *Cache) normalizeExhaustedResultError(ctx context.Context, res *sharedResult, err error) error {
	if err == nil {
		return err
	}
	// Transient unavailability drops nothing: this use receives an honest
	// error without the sentinel, and the row's next demand retries.
	if errors.Is(err, errSourcesUnavailable) {
		return fmt.Errorf("cached result %d could not be materialized right now; retrying may succeed (%s)", res.id, err.Error())
	}
	if !errors.Is(err, errSourcesExhausted) {
		return err
	}
	dropErr := c.dropExhaustedResult(ctx, res)
	return errors.Join(
		fmt.Errorf("cached result %d can no longer be materialized and was dropped from the cache; retrying will re-execute it (%s)", res.id, err.Error()),
		dropErr,
	)
}

func (c *Cache) removePersistedEdge(ctx context.Context, resultID sharedResultID) (bool, error) {
	if c == nil || resultID == 0 {
		return false, nil
	}

	var (
		res        *sharedResult
		queue      []*sharedResult
		onReleases []OnReleaseFunc
		rerr       error
	)
	c.egraphMu.Lock()
	if _, found := c.persistedEdgesByResult[resultID]; !found {
		c.egraphMu.Unlock()
		return false, nil
	}
	delete(c.persistedEdgesByResult, resultID)
	res = c.resultsByID[resultID]
	if res != nil {
		var err error
		queue, err = c.decrementIncomingOwnershipLocked(ctx, res, queue)
		rerr = errors.Join(rerr, err)
	}
	collectReleases, collectErr := c.collectUnownedResultsLocked(ctx, queue)
	onReleases = append(onReleases, collectReleases...)
	rerr = errors.Join(rerr, collectErr)
	c.egraphMu.Unlock()

	return true, errors.Join(rerr, runOnReleaseFuncs(ctx, onReleases))
}

func (c *Cache) incrementIncomingOwnershipLocked(ctx context.Context, res *sharedResult) {
	if c == nil || res == nil {
		return
	}
	res.incomingOwnershipCount++
	c.traceRefAcquired(ctx, res, res.incomingOwnershipCount)
}

func (c *Cache) enqueueCollectibleResultLocked(queue []*sharedResult, res *sharedResult) []*sharedResult {
	if c == nil || res == nil || res.id == 0 {
		return queue
	}
	if c.resultsByID[res.id] != res {
		return queue
	}
	if res.incomingOwnershipCount != 0 {
		return queue
	}
	return append(queue, res)
}

func (c *Cache) decrementIncomingOwnershipLocked(ctx context.Context, res *sharedResult, queue []*sharedResult) ([]*sharedResult, error) {
	if c == nil || res == nil {
		return queue, nil
	}
	res.incomingOwnershipCount--
	c.traceRefReleased(ctx, res, res.incomingOwnershipCount)
	if res.incomingOwnershipCount < 0 {
		c.traceRefUnderflow(ctx, res, res.incomingOwnershipCount)
		return queue, fmt.Errorf("incoming ownership underflow for result %d", res.id)
	}
	return c.enqueueCollectibleResultLocked(queue, res), nil
}

func (c *Cache) collectUnownedResultsLocked(ctx context.Context, queue []*sharedResult) ([]OnReleaseFunc, error) {
	if c == nil {
		return nil, nil
	}

	var (
		rerr       error
		onReleases []OnReleaseFunc
	)

	for len(queue) > 0 {
		res := queue[len(queue)-1]
		queue = queue[:len(queue)-1]

		if c.resultsByID[res.id] != res {
			continue
		}
		if res.incomingOwnershipCount != 0 {
			continue
		}

		depIDs := make([]sharedResultID, 0, len(res.deps))
		for depID := range res.deps {
			depIDs = append(depIDs, depID)
		}

		c.removeResultFromEgraphLocked(ctx, res)
		if res.onRelease != nil {
			onReleases = append(onReleases, res.onRelease)
		}
		res.deps = nil
		res.depParents = nil

		for _, depID := range depIDs {
			c.forgetDependencyEdgeLocked(res.id, depID)
			depRes := c.resultsByID[depID]
			if depRes == nil {
				continue
			}
			c.traceDependencyRemoved(ctx, res.id, depID, "parent_collected")
			var err error
			queue, err = c.decrementIncomingOwnershipLocked(ctx, depRes, queue)
			rerr = errors.Join(rerr, err)
		}
	}

	return onReleases, rerr
}

func runOnReleaseFuncs(ctx context.Context, onReleases []OnReleaseFunc) error {
	var rerr error
	for _, onRelease := range onReleases {
		if onRelease == nil {
			continue
		}
		rerr = errors.Join(rerr, onRelease(ctx))
	}
	return rerr
}

func resultSnapshotLeaseID(resultID sharedResultID, role string) string {
	return fmt.Sprintf("dagql/result/%d/%s", resultID, url.PathEscape(role))
}

func joinOnRelease(a, b OnReleaseFunc) OnReleaseFunc {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	default:
		return func(ctx context.Context) error {
			return errors.Join(a(ctx), b(ctx))
		}
	}
}

type snapshotOwnerKey struct {
	Role string
}

func desiredSnapshotLinksForResult(res *sharedResult) []PersistedSnapshotRefLink {
	if res == nil {
		return nil
	}

	state := res.loadPayloadState()
	if state.realized && state.self != nil {
		return snapshotOwnerLinksFromTyped(state.self)
	}

	if len(state.snapshotOwnerLinks) == 0 {
		return nil
	}
	return slices.Clone(state.snapshotOwnerLinks)
}

func (c *Cache) resultSnapshotLeaseCleanup(res *sharedResult) OnReleaseFunc {
	if c == nil || c.snapshotManager == nil || res == nil {
		return nil
	}

	return func(ctx context.Context) error {
		if res.id == 0 {
			return nil
		}

		links := res.loadSnapshotOwnerLinks()

		seen := make(map[snapshotOwnerKey]struct{}, len(links))
		var rerr error
		for _, link := range links {
			key := snapshotOwnerKey{Role: link.Role}
			if _, alreadySeen := seen[key]; alreadySeen {
				continue
			}
			seen[key] = struct{}{}
			rerr = errors.Join(rerr, c.snapshotManager.RemoveLease(
				ctx,
				resultSnapshotLeaseID(res.id, link.Role),
			))
		}
		return rerr
	}
}

func (c *Cache) syncResultSnapshotLeases(ctx context.Context, res *sharedResult) error {
	if c == nil || c.snapshotManager == nil || res == nil || res.id == 0 {
		return nil
	}

	links := desiredSnapshotLinksForResult(res)

	oldLinks := res.loadSnapshotOwnerLinks()

	oldByKey := make(map[snapshotOwnerKey]PersistedSnapshotRefLink, len(oldLinks))
	newByKey := make(map[snapshotOwnerKey]PersistedSnapshotRefLink, len(links))

	for _, link := range oldLinks {
		oldByKey[snapshotOwnerKey{Role: link.Role}] = link
	}
	for _, link := range links {
		key := snapshotOwnerKey{Role: link.Role}
		if prev, found := newByKey[key]; found && prev.RefKey != link.RefKey {
			return fmt.Errorf(
				"sync result %d snapshot owner leases: conflicting desired links for %q: %q vs %q",
				res.id,
				key.Role,
				prev.RefKey,
				link.RefKey,
			)
		}
		newByKey[key] = link
	}

	for key, oldLink := range oldByKey {
		newLink, ok := newByKey[key]
		if !ok || newLink.RefKey != oldLink.RefKey {
			if err := c.snapshotManager.RemoveLease(
				ctx,
				resultSnapshotLeaseID(res.id, key.Role),
			); err != nil {
				return err
			}
		}
	}

	for key, newLink := range newByKey {
		oldLink, ok := oldByKey[key]
		if !ok || oldLink.RefKey != newLink.RefKey {
			if err := c.snapshotManager.AttachLease(
				ctx,
				resultSnapshotLeaseID(res.id, key.Role),
				newLink.RefKey,
			); err != nil {
				return err
			}
		}
	}

	newLinks := make([]PersistedSnapshotRefLink, 0, len(newByKey))
	for _, link := range newByKey {
		newLinks = append(newLinks, link)
	}
	res.storeSnapshotOwnerLinks(newLinks)

	return nil
}

func (c *Cache) SyncResultSnapshotOwnerLeases(ctx context.Context, res AnyResult) error {
	if c == nil || res == nil {
		return nil
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return nil
	}
	return c.syncResultSnapshotLeases(ctx, shared)
}

// sweepAllDaggerOwnerLeases removes every dagql-owned snapshot lease. A
// wiped store retains nothing, so no lease may survive the wipe — including
// leases attached by a restore-vetting pass that aborted before its own
// stale sweep could run.
func (c *Cache) sweepAllDaggerOwnerLeases(ctx context.Context) error {
	if c.snapshotManager == nil {
		return nil
	}
	if err := c.snapshotManager.DeleteStaleDaggerOwnerLeases(ctx, nil); err != nil {
		return fmt.Errorf("sweep dagql owner leases after wipe: %w", err)
	}
	return nil
}

func prepareCacheDBs(ctx context.Context, dbPath string) (*sql.DB, *persistdb.Queries, error) {
	connURL := &url.URL{
		Scheme: "file",
		Path:   dbPath,
		RawQuery: url.Values{
			"_pragma": []string{ // ref: https://www.sqlite.org/pragma.html
				// WAL mode for better concurrency behavior and performance
				"journal_mode=WAL",

				// wait up to 10s when there are concurrent writers
				"busy_timeout=10000",

				// for now, it's okay if we lose cache after a catastrophic crash
				// (it's just a cache afterall), we'll take the better performance
				"synchronous=OFF",

				// other pragmas to possible worth consideration someday:
				// cache_size
				// threads
				// optimize
			},
			"_txlock": []string{"immediate"}, // use BEGIN IMMEDIATE for transactions
		}.Encode(),
	}
	db, err := sql.Open("sqlite", connURL.String())
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", connURL, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("ping %s: %w", connURL, err)
	}
	if _, err := db.Exec(persistdb.Schema); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("migrate persistence schema: %w", err)
	}
	persistDB, err := persistdb.Prepare(ctx, db)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("prepare persistence queries: %w", err)
	}

	return db, persistDB, nil
}

func closeCacheDBs(db *sql.DB, persistDB *persistdb.Queries) error {
	var err error
	if persistDB != nil {
		err = errors.Join(err, persistDB.Close())
	}
	if db != nil {
		err = errors.Join(err, db.Close())
	}
	return err
}

func RemoveCachePersistenceStore(dbPath string) error {
	return wipeSQLiteFiles(dbPath)
}

func wipeSQLiteFiles(dbPath string) error {
	removeIfExists := func(path string) error {
		err := os.Remove(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := removeIfExists(dbPath); err != nil {
		return err
	}
	if err := removeIfExists(dbPath + "-wal"); err != nil {
		return err
	}
	if err := removeIfExists(dbPath + "-shm"); err != nil {
		return err
	}
	return nil
}

type Cache struct {
	// callsMu protects in-flight call bookkeeping and arbitrary in-memory call maps.
	callsMu sync.Mutex
	// sessionMu protects per-session tracked cache-backed results and arbitrary values.
	sessionMu sync.Mutex
	// egraphMu protects all e-graph state and indexes.
	egraphMu sync.RWMutex

	persistenceResetReason CachePersistenceResetReason

	// restoreSummary records the boot-restore outcome (kept/dropped/wiped);
	// guarded by egraphMu after the boot writes it.
	restoreSummary *CacheRestoreSummary
	// serveStats are the warm-serving counters; statsFilePath is where they
	// flush at shutdown, next to the persisted store.
	serveStats    cacheServeStats
	statsFilePath string
	// importedResultCount is how many persisted rows survived restore
	// vetting this boot; freshResultCount counts results published from
	// work executed this boot. Flush writes both, plus the total, as the
	// self-check that importing and re-exporting a store adds no rows.
	importedResultCount int64
	freshResultCount    atomic.Int64

	// calls that are in progress, keyed by a combination of the call key and the concurrency key
	// two calls with the same call+concurrency key will be "single-flighted" (only one will actually run)
	ongoingCalls map[callConcurrencyKeys]*ongoingCall

	//
	// indexes for eq classes, which are disjoint sets of digests considered equivalent and interchangeable
	//

	nextEgraphClassID eqClassID

	// map of eqClassID -> all digests in that class
	eqClassToDigests map[eqClassID]map[string]struct{}

	// map of eqClassID -> all labeled extra digests known to belong to that class
	eqClassExtraDigests map[eqClassID]map[call.ExtraDigest]struct{}

	// map of digest -> eqClassID for the class that digest is in, if any
	// due to the sets being disjoint, a digest is enforced to only be in one
	// set at a time (any overlap results in union of the sets)
	egraphDigestToClass map[string]eqClassID

	// the parent of the given eqClassID, slice is index by eqClassID so it's
	// conceptually a map of eqClassID->parent eqClassID
	egraphParents []eqClassID

	// the rank of the given eqClassID, slice is index by eqClassID so it's
	// conceptually a map of eqClassID->rank
	egraphRanks []uint8

	//
	// indexes for terms
	//

	nextEgraphTermID egraphTermID

	// term ID -> term
	egraphTerms map[egraphTermID]*egraphTerm

	// term digest -> all terms with that digest
	egraphTermsByTermDigest map[string]*set.TreeSet[egraphTermID]

	//
	// indexes for results
	//

	nextSharedResultID sharedResultID

	// maxAllocatedResultID is the allocator high-water mark: the maximum
	// result ID ever allocated in this store's lifetime. Unlike
	// nextSharedResultID it is monotone — it survives e-graph drain resets
	// in memory and restores from meta at boot — so an ID bound to an
	// exported origin can never be re-allocated to a different result.
	maxAllocatedResultID sharedResultID

	// result id -> result
	resultsByID map[sharedResultID]*sharedResult

	// resultsByOrigin indexes results by their durable origin pair. It is
	// the bundle-import dedup gate: a row whose origin is already present
	// is never re-created.
	resultsByOrigin map[resultOrigin]sharedResultID

	// map of eq class -> all terms that have it as an input, needed during repair to
	// figure out all the terms that need repair after eq class union
	inputEqClassToTerms map[eqClassID]map[egraphTermID]struct{}

	// reverse index from canonical output eq class to all terms whose outputs are
	// currently represented by that class
	outputEqClassToTerms map[eqClassID]map[egraphTermID]struct{}

	// reverse index from materialized result to all output eq classes it is
	// currently associated with
	resultOutputEqClasses map[sharedResultID]map[eqClassID]struct{}

	// explicit result<->term associations. These are distinct from output eq
	// class membership: multiple results can share an output eq class, but
	// cache lookup for a matched term should first prefer results that were
	// actually observed for that term before falling back to equivalent outputs.
	termResults map[egraphTermID]map[sharedResultID]egraphResultTermAssoc
	resultTerms map[sharedResultID]map[egraphTermID]struct{}

	// Reverse index from any known result-associated digest to materialized results.
	// This includes request recipe+extra digests and result recipe+extra digests.
	egraphResultsByDigest map[string]*set.TreeSet[sharedResultID]

	// Explicit retained-root edges for persisted results.
	persistedEdgesByResult map[sharedResultID]persistedEdge

	// per-term input provenance indicates whether each input slot was
	// result-backed or digest-only when the term was observed
	termInputProvenance map[egraphTermID][]egraphInputProvenanceKind

	// in-progress and completed opaque in-memory calls, keyed by call key
	ongoingArbitraryCalls   map[string]*sharedArbitraryResult
	completedArbitraryCalls map[string]*sharedArbitraryResult

	sessionResultIDsBySession         map[string]map[sharedResultID]struct{}
	sessionArbitraryCallKeysBySession map[string]map[string]struct{}
	sessionLazySpansBySession         map[string]map[sharedResultID]trace.SpanContext
	// sessionResultInstallSpans records which API spans returned/own which
	// results in a session. Lazy resume spans cause-link the install spans of
	// the result being evaluated so dagui can resolve pending state and mark
	// owners caused-failed. Direct installs are recorded per result (owned
	// dependency edges copy the owning span onto the dep at attach time);
	// module API call returns are recorded once on the returned result with
	// ownsClosure set, and lookups resolve closure ownership on demand by
	// walking dep edges upward via depParents.
	sessionResultInstallSpans    map[string]map[sharedResultID]map[string]sessionResultInstallSpan
	sessionResourcesBySession    map[string]map[SessionResourceHandle]*sessionResourceBindings
	sessionHandlesBySession      map[string]*set.TreeSet[SessionResourceHandle]
	sessionVolatileVarsBySession map[string]map[string]string

	sqlDB *sql.DB
	// persistent normalized cache store (disk persistence/import).
	pdb *persistdb.Queries

	// storeUUID is this persistence store's identity: minted once at store
	// creation, persisted in meta, wiped with the store. It is the first
	// half of every locally-minted result origin. Empty when persistence
	// is disabled (no DB path).
	storeUUID string

	traceBootID     string
	traceSeq        uint64
	traceImportRuns uint64

	snapshotManager bkcache.SnapshotManager
	snapshotGC      func(context.Context) error

	// contentChainBlobSource serves content-chain blobs the local content
	// store is missing. Set once during the boot window, before serving
	// (R14); nil means chains realize from local content alone (a blob
	// absent locally is then a permanently missing blob for this boot).
	contentChainBlobSource bkcache.BlobSource

	closeOnce sync.Once
	closeErr  error
}

// SetContentChainBlobSource wires the transport content-chain realization
// fetches missing blobs through. Must be called during the boot window,
// before the cache serves.
func (c *Cache) SetContentChainBlobSource(src bkcache.BlobSource) {
	if c == nil {
		return
	}
	c.contentChainBlobSource = src
}

type callConcurrencyKeys struct {
	callKey        string
	concurrencyKey string
}

type OnReleaseFunc = func(context.Context) error

type sharedResultID uint64

type sessionResourceBindings struct {
	latestClientID string
	byClientID     map[string]any
}

type SessionResourceCandidate struct {
	ClientID string
	Value    any
}

func compareSessionResourceHandles(a, b SessionResourceHandle) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func compareSharedResults(a, b *sharedResult) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	default:
		return compareSharedResultID(a.id, b.id)
	}
}

func compareSpanContexts(a, b trace.SpanContext) int {
	aTraceID := a.TraceID()
	bTraceID := b.TraceID()
	if cmp := bytes.Compare(aTraceID[:], bTraceID[:]); cmp != 0 {
		return cmp
	}
	aSpanID := a.SpanID()
	bSpanID := b.SpanID()
	return bytes.Compare(aSpanID[:], bSpanID[:])
}

func spanContextKey(ctx trace.SpanContext) string {
	return ctx.TraceID().String() + "/" + ctx.SpanID().String()
}

// CacheUsageSizeProvider resolves concrete snapshot sizes for cache usage accounting.
type CacheUsageSizeProvider interface {
	SnapshotSize(context.Context, string) (int64, error)
}

type cacheUsageSizer interface {
	// CacheUsageSize returns the concrete size of the cached payload when known.
	// ok=false means "size is currently unknown/not available".
	CacheUsageSize(context.Context, CacheUsageSizeProvider, string) (sizeBytes int64, ok bool, err error)
}

type hasCacheUsageIdentity interface {
	// CacheUsageIdentities returns the stable identities for deduplicating
	// physical storage accounting across cache results that share snapshots.
	CacheUsageIdentities() []string
}

type cacheUsageMayChange interface {
	// CacheUsageMayChange reports whether usage size can change over time for the
	// same usage identity (for example mutable cache volume snapshots).
	CacheUsageMayChange() bool
}

// materializationState is the single home for a result's content state: what
// the result currently has in memory, and what it can be (re)made from. It
// lives on the sharedResult — the one object with the same lifetime as the
// result's identity — and is written only at the cache's decision points:
// publication of locally completed work, import at boot, and the result's
// own materialization outcome (decode or lazy realization). Flush only reads
// it. Guarded by the owning sharedResult's payloadMu.
type materializationState struct {
	// envelope is the persisted form of this result's value. Present iff the
	// result was restored from persistence and not yet decoded.
	envelope *PersistedResultEnvelope

	// sources is what this result's content can be (re)materialized from, in
	// fall-through order (see retainedSourceKind). Content state is never
	// shared across results: these sources belong to this result alone.
	sources []retainedSource

	// realized reports whether the in-memory value payload is currently
	// usable; it distinguishes "initialized (possibly with a nil value)"
	// from "not initialized". Set only by this result's own publication,
	// decode, or lazy realization.
	realized bool
}

// retainedSourceKind identifies one way a result's content can be
// (re)materialized. Declaration order is the fall-through order used when a
// result must be realized: local snapshot first, then the content chain,
// then the persisted lazy form. Local bytes always win; pulling available
// content beats re-executing a recipe, whose realization recurses into
// demand-driven input materialization. Kinds are never persisted.
type retainedSourceKind uint8

const (
	// sourceLocalSnapshot is content already present in the local
	// snapshotter, identified by refKeys and kept alive by leases. RefKeys
	// are engine-local names and never cross an engine boundary.
	sourceLocalSnapshot retainedSourceKind = iota + 1

	// sourceContentChain is content reconstructible from content-addressed
	// layer chains: per snapshot role, an ordered layer list whose blobs a
	// CAS serves by digest. Realizing it fetches the missing blobs, applies
	// them as local snapshot layers, and installs the result as this row's
	// local-snapshot source — content arrives once, then serving is local.
	sourceContentChain

	// sourceLazyValue is the value's persisted lazy form: a registered lazy
	// struct referencing its input results, re-run through the existing lazy
	// evaluation path.
	sourceLazyValue
)

func (k retainedSourceKind) String() string {
	switch k {
	case sourceLocalSnapshot:
		return "local_snapshot"
	case sourceContentChain:
		return "content_chain"
	case sourceLazyValue:
		return "lazy_value"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(k))
	}
}

// PersistedContentChainLayer is one layer of a content chain: the
// uncompressed diff identity plus the compressed blob a CAS serves.
type PersistedContentChainLayer struct {
	DiffID    string `json:"diffID"`
	Blob      string `json:"blob"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

// PersistedResultContentChain is the content-chain identity for one of a
// result's snapshot roles: the ordered layer list that reconstructs the
// role's snapshot, identified by its containerd chainID. It is portable by
// construction (digests only, no engine-local names) and is what crosses in
// bundle manifests and persists locally in result_content_chains.
type PersistedResultContentChain struct {
	Role    string
	ChainID string
	Layers  []PersistedContentChainLayer
}

func (chain PersistedResultContentChain) clone() PersistedResultContentChain {
	cp := chain
	cp.Layers = slices.Clone(chain.Layers)
	return cp
}

// bkSnapshotChain converts to the snapshot manager's chain shape.
func (chain PersistedResultContentChain) bkSnapshotChain() bkcache.SnapshotChain {
	out := bkcache.SnapshotChain{ChainID: digest.Digest(chain.ChainID)}
	for _, layer := range chain.Layers {
		out.Layers = append(out.Layers, bkcache.ChainLayer{
			DiffID:    digest.Digest(layer.DiffID),
			Blob:      digest.Digest(layer.Blob),
			Size:      layer.Size,
			MediaType: layer.MediaType,
		})
	}
	return out
}

func cloneContentChains(chains []PersistedResultContentChain) []PersistedResultContentChain {
	if len(chains) == 0 {
		return nil
	}
	out := make([]PersistedResultContentChain, len(chains))
	for i := range chains {
		out[i] = chains[i].clone()
	}
	return out
}

// retainedSource is one entry in a result's fall-through source list,
// carrying the kind-specific identity needed to realize from it.
type retainedSource struct {
	kind retainedSourceKind

	// snapshotLinks is the identity for sourceLocalSnapshot: the local
	// snapshotter refKeys holding this result's content. Empty for other
	// kinds.
	snapshotLinks []PersistedSnapshotRefLink

	// contentChains is the identity for sourceContentChain: per snapshot
	// role, the layer chain that reconstructs it from CAS blobs. Empty for
	// other kinds.
	contentChains []PersistedResultContentChain

	// lazyFragment is the identity for sourceLazyValue: the value's
	// serialized deferred work, captured at publication (before realization
	// destroys the live recipe) or copied from the envelope at import.
	lazyFragment *PersistedLazyFragment

	// nonViable marks a source whose last realization attempt failed
	// permanently for this boot (e.g. a chain blob the CAS no longer has).
	// The walk skips non-viable sources; the identity stays — availability
	// is not identity, so flush still persists it and the next boot retries.
	nonViable bool
}

// ensureSource returns the source of the given kind, inserting it at its
// fall-through position if absent.
func (m *materializationState) ensureSource(kind retainedSourceKind) *retainedSource {
	idx := 0
	for idx < len(m.sources) && m.sources[idx].kind < kind {
		idx++
	}
	if idx < len(m.sources) && m.sources[idx].kind == kind {
		return &m.sources[idx]
	}
	m.sources = slices.Insert(m.sources, idx, retainedSource{kind: kind})
	return &m.sources[idx]
}

// setLocalSnapshotSource replaces the local-snapshot source's refKey links.
// Empty links remove the source: content that is not in the local
// snapshotter is not a local-snapshot source.
func (m *materializationState) setLocalSnapshotSource(links []PersistedSnapshotRefLink) {
	if len(links) == 0 {
		m.sources = slices.DeleteFunc(m.sources, func(src retainedSource) bool {
			return src.kind == sourceLocalSnapshot
		})
		return
	}
	m.ensureSource(sourceLocalSnapshot).snapshotLinks = slices.Clone(links)
}

// appendLocalSnapshotLink adds one refKey link to the local-snapshot source,
// creating the source if absent.
func (m *materializationState) appendLocalSnapshotLink(link PersistedSnapshotRefLink) {
	src := m.ensureSource(sourceLocalSnapshot)
	src.snapshotLinks = append(src.snapshotLinks, link)
}

// localSnapshotLinks returns a copy of the local-snapshot source's refKey
// links, or nil when the source is absent.
func (m *materializationState) localSnapshotLinks() []PersistedSnapshotRefLink {
	for i := range m.sources {
		if m.sources[i].kind == sourceLocalSnapshot {
			return slices.Clone(m.sources[i].snapshotLinks)
		}
	}
	return nil
}

// setContentChainSource replaces the content-chain source's identity.
// Empty chains remove the source.
func (m *materializationState) setContentChainSource(chains []PersistedResultContentChain) {
	if len(chains) == 0 {
		m.sources = slices.DeleteFunc(m.sources, func(src retainedSource) bool {
			return src.kind == sourceContentChain
		})
		return
	}
	m.ensureSource(sourceContentChain).contentChains = cloneContentChains(chains)
}

// unionContentChains adds chain identities for roles the content-chain
// source does not already carry. Existing roles are never overwritten:
// same-origin observations may only add, first-imported wins per role.
func (m *materializationState) unionContentChains(chains []PersistedResultContentChain) {
	if len(chains) == 0 {
		return
	}
	src := m.ensureSource(sourceContentChain)
	existing := make(map[string]struct{}, len(src.contentChains))
	for _, chain := range src.contentChains {
		existing[chain.Role] = struct{}{}
	}
	for _, chain := range chains {
		if _, present := existing[chain.Role]; present {
			continue
		}
		existing[chain.Role] = struct{}{}
		src.contentChains = append(src.contentChains, chain.clone())
	}
	if len(src.contentChains) == 0 {
		m.setContentChainSource(nil)
	}
}

// contentChains returns a copy of the content-chain source's identity, or
// nil when the source is absent.
func (m *materializationState) contentChains() []PersistedResultContentChain {
	for i := range m.sources {
		if m.sources[i].kind == sourceContentChain {
			return cloneContentChains(m.sources[i].contentChains)
		}
	}
	return nil
}

// viableContentChains returns a copy of the content-chain source's identity
// when the source exists and has not been marked non-viable this boot.
func (m *materializationState) viableContentChains() []PersistedResultContentChain {
	for i := range m.sources {
		if m.sources[i].kind == sourceContentChain {
			if m.sources[i].nonViable {
				return nil
			}
			return cloneContentChains(m.sources[i].contentChains)
		}
	}
	return nil
}

// markContentChainNonViable applies reset §9 D2's marking rule to the chain
// source: permanent realization failures (a blob the CAS no longer has,
// corrupt bytes) stop retries for this boot. The identity stays — flush
// still persists it and the next boot retries.
func (m *materializationState) markContentChainNonViable() {
	for i := range m.sources {
		if m.sources[i].kind == sourceContentChain {
			m.sources[i].nonViable = true
			return
		}
	}
}

// setLazyFragment records the value's serialized deferred work as the
// lazy-value source's identity.
func (m *materializationState) setLazyFragment(frag *PersistedLazyFragment) {
	if frag == nil || len(frag.JSON) == 0 {
		return
	}
	m.ensureSource(sourceLazyValue).lazyFragment = frag
}

// lazyFragment returns a copy of the lazy-value source's fragment, or nil
// when the source is absent or carries no payload.
func (m *materializationState) lazyFragment() *PersistedLazyFragment {
	for i := range m.sources {
		if m.sources[i].kind == sourceLazyValue {
			return m.sources[i].lazyFragment.clone()
		}
	}
	return nil
}

// sourceKinds returns the kinds present, in fall-through order.
func (m *materializationState) sourceKinds() []retainedSourceKind {
	if len(m.sources) == 0 {
		return nil
	}
	kinds := make([]retainedSourceKind, len(m.sources))
	for i := range m.sources {
		kinds[i] = m.sources[i].kind
	}
	return kinds
}

// servable reports whether this state can still honor a cache hit: the value
// is realized, a persisted envelope remains to decode, or at least one
// retained source remains to re-make the value from. A state with none of
// these has nothing to deliver and must not be served as a hit.
func (m *materializationState) servable() bool {
	return m.realized || m.envelope != nil || len(m.sources) > 0
}

// clone returns a copy sharing no mutable slices with the original.
func (m *materializationState) clone() materializationState {
	cp := *m
	cp.sources = nil
	if len(m.sources) > 0 {
		cp.sources = make([]retainedSource, len(m.sources))
		for i := range m.sources {
			cp.sources[i] = m.sources[i]
			cp.sources[i].snapshotLinks = slices.Clone(m.sources[i].snapshotLinks)
			cp.sources[i].contentChains = cloneContentChains(m.sources[i].contentChains)
			cp.sources[i].lazyFragment = m.sources[i].lazyFragment.clone()
		}
	}
	return cp
}

// resultOrigin is a result's durable cross-engine identity: the store where
// the result was first created plus its result ID there. Minted exactly once
// at first publication and preserved verbatim through every export/import
// hop; import dedups rows by this pair. Origin never participates in lookup
// identity — digests and terms do that.
type resultOrigin struct {
	storeUUID string
	resultID  uint64
}

func (o resultOrigin) isZero() bool {
	return o.storeUUID == "" && o.resultID == 0
}

// sharedResult holds cache-entry state and shared payload published to per-call Result values.
type sharedResult struct {
	// id is the stable cache-local identity for this materialized result.
	id sharedResultID

	// origin is this result's durable origin pair. For locally-minted
	// results it is (the store's UUID, id); for bundle-imported results it
	// is the pair carried in the bundle. Set once, under egraphMu, at ID
	// assignment / restore / bundle import; immutable afterward.
	origin resultOrigin

	// Immutable payload shared by all per-call Result values.
	self     Typed
	isObject bool
	// objClass is the ObjectType originally used to wrap this result the
	// first time it became an AnyObjectResult. Reconstruction reuses it
	// directly so the cache does not need to resolve the concrete type
	// by name (which costs a ModDepsForCall + Schema build for results
	// whose type lives in a module that is not installed in the caller's
	// schema). Nil when the result has not yet been wrapped as an object
	// (e.g. just imported from persistence and not yet decoded).
	objClass ObjectType
	// resultCall is the non-lossy semantic/provenance call-node metadata
	// for this materialized result. It is used for canonical recipe
	// reconstruction and telemetry hierarchy reconstruction, not execution or
	// liveness.
	//
	// Cache-owned frames remain immutable once published. The mutable part is
	// which frame is currently published for this shared result.
	resultCallMu sync.RWMutex
	resultCall   *ResultCall
	// payloadMu guards lazy payload publication for imported persisted hits and
	// prune-accounting timestamps that can change after initial publication.
	payloadMu sync.RWMutex
	// materialization is the single home for this result's content state:
	// the persisted envelope, the retained sources the content can be
	// (re)made from, and whether the in-memory value payload is usable.
	// Guarded by payloadMu.
	materialization materializationState
	onRelease       OnReleaseFunc
	// deps tracks exact materialized child-result dependencies used for
	// release/liveness propagation and persistence closure. This includes
	// explicit out-of-band deps and exact resultCall refs mirrored into deps
	// during materialization.
	deps map[sharedResultID]struct{}
	// depParents is the reverse index for direct deps. It lets install-span
	// lookups walk dep edges upward on demand to find closure-owning installs
	// (module API call returns) for the result being evaluated.
	depParents *set.TreeSet[sharedResultID]
	// sessionResourceHandle is set when this result is itself an attached
	// session-resource handle leaf. requiredSessionResources is the flattened
	// transitive set of handle requirements for cache-hit validation.
	sessionResourceHandle    SessionResourceHandle
	requiredSessionResources *set.TreeSet[SessionResourceHandle]

	// expiresAtUnix is the in-memory TTL deadline for cache-hit eligibility.
	// 0 means "never expires".
	expiresAtUnix int64

	// Prune-accounting metadata. Sizes are unknown until explicitly measured.
	createdAtUnixNano        int64
	lastUsedAtUnixNano       int64
	cacheUsageSizeByIdentity map[string]int64
	cacheUsageRecordTypeByID map[string]string
	description              string
	recordType               string

	// incomingOwnershipCount is the authoritative liveness count derived from
	// session edges, persisted edges, and result dependency edges.
	incomingOwnershipCount int64

	attachDepsMu     sync.Mutex
	attachDepsWaitCh chan struct{}
	attachDepsErr    error

	// materialize* is the one wait protocol for this result's
	// materialization work — decoding the persisted envelope through the
	// retained-source walk, and running pending deferred work. One runner,
	// N waiters, last-waiter-cancels, retry-on-failure. The two phases
	// never overlap in demand: value materialization only runs while the
	// payload is unrealized, deferred work only after it is realized.
	materializeMu      sync.Mutex
	lazyEval           LazyEvalFunc
	lazyEvalComplete   bool
	materializeWaitCh  chan struct{}
	materializeCancel  context.CancelCauseFunc
	materializeWaiters int
	materializeErr     error

	// restored marks a result created from persisted state at boot;
	// pendingWorkFromRestore marks that its deferred work was re-attached
	// from the persisted lazy fragment, so a permanent failure of that work
	// is retained-source exhaustion rather than a live call's own failure.
	restored               bool
	pendingWorkFromRestore bool
	// dropped marks a result removed from future servability after its
	// sources were permanently exhausted; guarded by egraphMu. Flush skips
	// dropped rows.
	dropped bool
}

type sharedResultPayloadState struct {
	self               Typed
	isObject           bool
	realized           bool
	servable           bool
	objClass           ObjectType
	persistedEnvelope  *PersistedResultEnvelope
	snapshotOwnerLinks []PersistedSnapshotRefLink
	contentChains      []PersistedResultContentChain
	sourceKinds        []retainedSourceKind
	createdAtUnixNano  int64
	lastUsedAtUnixNano int64
}

func (res *sharedResult) loadResultCall() *ResultCall {
	if res == nil {
		return nil
	}
	res.resultCallMu.RLock()
	frame := res.resultCall
	res.resultCallMu.RUnlock()
	return frame
}

func (res *sharedResult) storeResultCall(frame *ResultCall) {
	if res == nil {
		return
	}
	res.resultCallMu.Lock()
	res.resultCall = frame
	res.resultCallMu.Unlock()
}

func (res *sharedResult) loadPayloadState() sharedResultPayloadState {
	if res == nil {
		return sharedResultPayloadState{}
	}
	res.payloadMu.RLock()
	state := sharedResultPayloadState{
		self:               res.self,
		isObject:           res.isObject,
		realized:           res.materialization.realized,
		servable:           res.materialization.servable(),
		objClass:           res.objClass,
		persistedEnvelope:  res.materialization.envelope,
		snapshotOwnerLinks: res.materialization.localSnapshotLinks(),
		contentChains:      res.materialization.contentChains(),
		sourceKinds:        res.materialization.sourceKinds(),
		createdAtUnixNano:  res.createdAtUnixNano,
		lastUsedAtUnixNano: res.lastUsedAtUnixNano,
	}
	res.payloadMu.RUnlock()
	return state
}

// setObjClass remembers the ObjectType used to wrap this result the first
// time it became an AnyObjectResult. Subsequent calls with a matching class
// are idempotent; calls with a different class (which would indicate
// inconsistent wrapping) are ignored to preserve the first observation.
func (res *sharedResult) setObjClass(class ObjectType) {
	if res == nil || class == nil {
		return
	}
	res.payloadMu.Lock()
	if res.objClass == nil {
		res.objClass = class
	}
	res.payloadMu.Unlock()
}

func (res *sharedResult) loadSnapshotOwnerLinks() []PersistedSnapshotRefLink {
	if res == nil {
		return nil
	}
	res.payloadMu.RLock()
	links := res.materialization.localSnapshotLinks()
	res.payloadMu.RUnlock()
	return links
}

func (res *sharedResult) cloneMaterializationState() materializationState {
	if res == nil {
		return materializationState{}
	}
	res.payloadMu.RLock()
	cp := res.materialization.clone()
	res.payloadMu.RUnlock()
	return cp
}

func (res *sharedResult) storeLazyFragment(frag *PersistedLazyFragment) {
	if res == nil {
		return
	}
	res.payloadMu.Lock()
	res.materialization.setLazyFragment(frag)
	res.payloadMu.Unlock()
}

func (res *sharedResult) loadLazyFragment() *PersistedLazyFragment {
	if res == nil {
		return nil
	}
	res.payloadMu.RLock()
	frag := res.materialization.lazyFragment()
	res.payloadMu.RUnlock()
	return frag
}

func (res *sharedResult) loadContentChains() []PersistedResultContentChain {
	if res == nil {
		return nil
	}
	res.payloadMu.RLock()
	chains := res.materialization.contentChains()
	res.payloadMu.RUnlock()
	return chains
}

func (res *sharedResult) loadViableContentChains() []PersistedResultContentChain {
	if res == nil {
		return nil
	}
	res.payloadMu.RLock()
	chains := res.materialization.viableContentChains()
	res.payloadMu.RUnlock()
	return chains
}

func (res *sharedResult) storeContentChains(chains []PersistedResultContentChain) {
	if res == nil {
		return
	}
	res.payloadMu.Lock()
	res.materialization.setContentChainSource(chains)
	res.payloadMu.Unlock()
}

func (res *sharedResult) markContentChainNonViable() {
	if res == nil {
		return
	}
	res.payloadMu.Lock()
	res.materialization.markContentChainNonViable()
	res.payloadMu.Unlock()
}

func (res *sharedResult) storeSnapshotOwnerLinks(links []PersistedSnapshotRefLink) {
	if res == nil {
		return
	}
	res.payloadMu.Lock()
	res.materialization.setLocalSnapshotSource(links)
	res.payloadMu.Unlock()
}

// resultIsObject classifies whether val should be treated as an object result
// for cache purposes. When it is, it also returns the class that wraps it, so
// callers can stash the class alongside isObject and the invariant
// "isObject ⇒ objClass != nil" holds for every shared result.
func resultIsObject(val AnyResult, resolver TypeResolver) (bool, ObjectType, error) {
	if resolver == nil {
		return false, nil, errors.New("type resolver is nil")
	}
	if val == nil {
		return false, nil, nil
	}
	if obj, ok := val.(AnyObjectResult); ok {
		return true, obj.ObjectType(), nil
	}
	typ := val.Type()
	if typ == nil || typ.Elem != nil || typ.Name() == "" {
		return false, nil, nil
	}
	objType, ok := resolver.ObjectType(typ.Name())
	if !ok {
		return false, nil, nil
	}
	// Not every value whose type name matches the object class is instantiable
	// as that class — a Nullable[*T] value has typ.Name() == T but isn't
	// directly a T, for instance. Treat instantiation failure as 'not an
	// object of this class' rather than as a hard error.
	if _, err := objType.New(val); err != nil {
		return false, nil, nil //nolint:nilerr // see comment above
	}
	return true, objType, nil
}

func sharedResultObjectTypeName(res *sharedResult, state sharedResultPayloadState) string {
	if res == nil || !state.isObject {
		return ""
	}
	if frame := res.loadResultCall(); frame != nil && frame.Type != nil && frame.Type.NamedType != "" {
		return frame.Type.NamedType
	}
	if state.persistedEnvelope != nil && state.persistedEnvelope.TypeName != "" {
		return state.persistedEnvelope.TypeName
	}
	if state.self != nil && state.self.Type() != nil {
		return state.self.Type().Name()
	}
	return ""
}

// resolverForSharedResultObject returns a resolver that can instantiate the
// cached object's concrete type, rebuilding a dependency-aware schema from
// the result's call graph if the current resolver does not have the type.
//
// This is the fallback path for object reconstruction; the common path reuses
// the class captured on the shared result at construction time (objClass).
// Persisted-envelope decoding still uses this directly because there is no
// in-memory value to derive a class from at decode time.
func resolverForSharedResultObject(ctx context.Context, resolver TypeResolver, res *sharedResult, typeName string) (TypeResolver, error) {
	if resolver == nil || res == nil || typeName == "" {
		return resolver, nil
	}
	if _, ok := resolver.ObjectType(typeName); ok {
		return resolver, nil
	}
	srv, ok := resolver.(*Server)
	if !ok || srv.resultServerForCall == nil {
		return resolver, nil
	}
	resultCall := res.loadResultCall()
	if resultCall == nil {
		return resolver, nil
	}
	resolved, err := srv.resultServerForCall(ctx, resultCall)
	if err != nil {
		return nil, fmt.Errorf("resolve schema for result %d type %q: %w", res.id, typeName, err)
	}
	if resolved == nil {
		return resolver, nil
	}
	return resolved, nil
}

func wrapSharedResultWithResolver(ctx context.Context, res *sharedResult, hitCache bool, resolver TypeResolver) (AnyResult, error) {
	ret := Result[Typed]{
		shared:   res,
		hitCache: hitCache,
	}
	if res == nil {
		return ret, nil
	}
	state := res.loadPayloadState()
	if !state.isObject {
		return ret, nil
	}
	typeName := sharedResultObjectTypeName(res, state)
	if state.self == nil {
		switch {
		case state.persistedEnvelope != nil:
			return nil, fmt.Errorf("reconstruct object result %q: persisted payload has not been decoded", typeName)
		case state.realized:
			return nil, fmt.Errorf("reconstruct object result %q: invalid payload state (realized=true, self=nil)", typeName)
		default:
			return nil, fmt.Errorf("reconstruct object result %q: missing typed payload", typeName)
		}
	}
	if typeName == "" {
		return nil, fmt.Errorf("reconstruct object result: missing type name")
	}
	// Prefer the current resolver's class so cache hits re-wrap against the
	// reading server (which may have its own per-view/per-server resolvers).
	if resolver != nil {
		if objType, ok := resolver.ObjectType(typeName); ok {
			return objType.New(ret)
		}
	}
	// Resolver doesn't know the type — typically a cross-module hit where the
	// concrete type lives in a module not installed in the caller's schema.
	// Reuse the class captured at result construction; it works regardless of
	// where the result is being read from.
	if state.objClass != nil && state.objClass.TypeName() == typeName {
		return state.objClass.New(ret)
	}
	if resolver == nil {
		return nil, fmt.Errorf("reconstruct object result %q: missing type resolver", typeName)
	}
	// Last resort: rebuild a dep-aware resolver from the result's call frame.
	// Reached when class capture missed a path (e.g., a value materialized in
	// core/object.go's ConvertFromSDKResult against a server that doesn't have
	// the producing module installed, or a persisted import loaded by ID
	// before any class-bearing wrap). The resolved class is cached back so
	// subsequent reconstructions skip this branch.
	depResolver, err := resolverForSharedResultObject(ctx, resolver, res, typeName)
	if err != nil {
		return nil, err
	}
	if depResolver != nil {
		if objType, ok := depResolver.ObjectType(typeName); ok {
			objRes, err := objType.New(ret)
			if err != nil {
				return nil, fmt.Errorf("reconstruct object result %q: %w", typeName, err)
			}
			res.setObjClass(objType)
			return objRes, nil
		}
	}
	return nil, fmt.Errorf("reconstruct object result %q: unknown object type", typeName)
}

// ongoingCall tracks one in-flight GetOrInitCall execution and points at the
// shared result payload that will be returned to waiters.
type ongoingCall struct {
	callConcurrencyKeys     callConcurrencyKeys
	isPersistable           bool
	ttlSeconds              int64
	initCompletedResultOnce sync.Once
	handoffHoldActive       bool
	initCompletedResultErr  error

	waitCh                     chan struct{}
	cancel                     context.CancelCauseFunc
	waiters                    int
	err                        error
	val                        AnyResult
	sharedWorkCtx              context.Context
	releaseSharedWorkLeaseFn   func(context.Context) error
	releaseSharedWorkLeaseOnce sync.Once

	res *sharedResult
}

func (oc *ongoingCall) releaseSharedWorkLease(ctx context.Context) error {
	if oc == nil || oc.releaseSharedWorkLeaseFn == nil {
		return nil
	}
	var err error
	oc.releaseSharedWorkLeaseOnce.Do(func() {
		err = oc.releaseSharedWorkLeaseFn(ctx)
	})
	return err
}

// newDetachedResult creates a non-cache-backed Result from an explicit call frame and value.
func newDetachedResult[T Typed](call *ResultCall, self T) Result[T] {
	var resultCall *ResultCall
	if call != nil {
		resultCall = call.clone()
	}
	return Result[T]{
		shared: &sharedResult{
			self:            self,
			resultCall:      resultCall,
			materialization: materializationState{realized: true},
		},
	}
}

func (c *Cache) normalizePendingResultCallRefs(ctx context.Context, frame *ResultCall) error {
	return c.normalizePendingResultCallRefsWithSeen(ctx, frame, map[*ResultCall]struct{}{})
}

func (c *Cache) canonicalEquivalentSharedResultLocked(sessionID string, res *sharedResult, nowUnix int64) *sharedResult {
	if res == nil || res.id == 0 {
		return nil
	}

	candidates := newSharedResultSet()
	for outputEqID := range c.outputEqClassesForResultLocked(res.id) {
		outputEqID = c.findEqClassLocked(outputEqID)
		if outputEqID == 0 {
			continue
		}
		for dig := range c.eqClassToDigests[outputEqID] {
			c.appendDigestResultsLocked(candidates, digest.Digest(dig), nowUnix)
		}
	}

	if candidates.Empty() {
		return res
	}
	if canonical := c.selectLookupCandidateForSessionLocked(sessionID, candidates); canonical != nil {
		return canonical
	}
	return res
}

func (c *Cache) normalizePendingResultCallRefsWithSeen(ctx context.Context, frame *ResultCall, seen map[*ResultCall]struct{}) error {
	if frame == nil {
		return nil
	}
	if _, ok := seen[frame]; ok {
		return fmt.Errorf("cycle while normalizing pending call refs")
	}
	seen[frame] = struct{}{}
	defer delete(seen, frame)

	if err := c.normalizePendingResultCallRefWithSeen(ctx, frame.Receiver, seen); err != nil {
		return fmt.Errorf("receiver: %w", err)
	}
	if frame.Module != nil {
		if err := c.normalizePendingResultCallRefWithSeen(ctx, frame.Module.ResultRef, seen); err != nil {
			return fmt.Errorf("module: %w", err)
		}
	}
	for _, arg := range frame.Args {
		if arg == nil {
			continue
		}
		if err := c.normalizePendingResultCallLiteralWithSeen(ctx, arg.Value, seen); err != nil {
			return fmt.Errorf("arg %q: %w", arg.Name, err)
		}
	}
	for _, input := range frame.ImplicitInputs {
		if input == nil {
			continue
		}
		if err := c.normalizePendingResultCallLiteralWithSeen(ctx, input.Value, seen); err != nil {
			return fmt.Errorf("implicit input %q: %w", input.Name, err)
		}
	}
	return nil
}

func (c *Cache) normalizePendingResultCallRefWithSeen(ctx context.Context, ref *ResultCallRef, seen map[*ResultCall]struct{}) error {
	if ref == nil {
		return nil
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Call == nil {
		return nil
	}
	if err := c.normalizePendingResultCallRefsWithSeen(ctx, ref.Call, seen); err != nil {
		return err
	}
	resultID, err := c.resultIDForCall(ref.Call)
	if err != nil {
		return err
	}
	ref.ResultID = uint64(resultID)
	if shared, _, _, err := c.sharedResultByResultID(ctx, "", resultID, sharedResultLookupExact); err == nil {
		ref.shared = shared
	}
	ref.Call = nil
	return nil
}

func (c *Cache) normalizePendingResultCallLiteralWithSeen(ctx context.Context, lit *ResultCallLiteral, seen map[*ResultCall]struct{}) error {
	if lit == nil {
		return nil
	}
	switch lit.Kind {
	case ResultCallLiteralKindResultRef:
		return c.normalizePendingResultCallRefWithSeen(ctx, lit.ResultRef, seen)
	case ResultCallLiteralKindList:
		for _, item := range lit.ListItems {
			if err := c.normalizePendingResultCallLiteralWithSeen(ctx, item, seen); err != nil {
				return err
			}
		}
	case ResultCallLiteralKindObject:
		for _, field := range lit.ObjectFields {
			if field == nil {
				continue
			}
			if err := c.normalizePendingResultCallLiteralWithSeen(ctx, field.Value, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Cache) AttachResult(ctx context.Context, sessionID string, resolver TypeResolver, res AnyResult) (AnyResult, error) {
	if sessionID == "" {
		return nil, errors.New("attach result: empty session ID")
	}
	return c.attachResult(ctx, sessionID, resolver, res)
}

func (c *Cache) attachResult(ctx context.Context, sessionID string, resolver TypeResolver, res AnyResult) (AnyResult, error) {
	if sessionID == "" {
		return nil, errors.New("attach result: empty session ID")
	}
	if resolver == nil {
		return nil, errors.New("attach result: type resolver is nil")
	}
	if res == nil {
		return nil, nil
	}
	shared := res.cacheSharedResult()
	if shared == nil {
		return nil, fmt.Errorf("attach dependency result: missing shared result")
	}
	if objVal, ok := res.(AnyObjectResult); ok {
		shared.setObjClass(objVal.ObjectType())
	}
	if shared.id != 0 {
		loaded, err := c.ensurePersistedHitValueLoaded(ctx, resolver, res)
		if err != nil {
			return nil, fmt.Errorf("attach dependency result: refresh cache-backed value: %w", c.normalizeExhaustedResultError(ctx, shared, err))
		}
		touchSharedResultLastUsed(shared, time.Now().UnixNano())
		c.traceAttachResultReusedCacheBacked(ctx, sessionID, shared)
		c.trackSessionResult(ctx, sessionID, loaded, true)
		return loaded, nil
	}
	frame := shared.loadResultCall()
	if frame == nil {
		return nil, fmt.Errorf("attach dependency result: missing result call frame")
	}
	req := &CallRequest{
		ResultCall: frame.clone(),
	}
	if err := c.normalizePendingResultCallRefs(ctx, req.ResultCall); err != nil {
		return nil, fmt.Errorf("attach dependency result: normalize pending result call refs: %w", err)
	}
	shared.storeResultCall(req.ResultCall)
	c.traceResultCallFrameUpdated(ctx, shared, "attach_result_normalized", frame, req.ResultCall)

	callDigest, err := req.deriveRecipeDigest(c)
	if err != nil {
		return nil, fmt.Errorf("attach dependency result: derive request digest: %w", err)
	}
	requestSelf, requestInputRefs, err := req.selfDigestAndInputRefs(c)
	if err != nil {
		return nil, fmt.Errorf("attach dependency result: derive request term digests: %w", err)
	}
	requestInputs := make([]digest.Digest, 0, len(requestInputRefs))
	for _, ref := range requestInputRefs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			return nil, fmt.Errorf("attach dependency result: derive request term input digest: %w", err)
		}
		requestInputs = append(requestInputs, dig)
	}

	hitRes, hit, err := c.lookupCacheForRequest(ctx, sessionID, resolver, req, callDigest, requestSelf, requestInputs, requestInputRefs)
	if err != nil {
		return nil, fmt.Errorf("attach dependency result: %w", err)
	}
	if hit {
		c.registerLazyEvaluation(hitRes.cacheSharedResult(), hitRes)
		return hitRes, nil
	}

	oc := &ongoingCall{
		val: res,
	}
	if err := c.initCompletedResult(ctx, resolver, oc, req, sessionID); err != nil {
		return nil, fmt.Errorf("attach dependency result: %w", err)
	}
	if oc.res == nil {
		return nil, fmt.Errorf("attach dependency result: completed without initialized result")
	}
	c.trackSessionResult(ctx, sessionID, Result[Typed]{shared: oc.res}, false)
	if oc.handoffHoldActive {
		c.egraphMu.Lock()
		queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
		collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
		c.egraphMu.Unlock()
		oc.handoffHoldActive = false
		if relErr := errors.Join(decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)); relErr != nil {
			return nil, fmt.Errorf("attach dependency result: release publication hold: %w", relErr)
		}
	}
	touchSharedResultLastUsed(oc.res, time.Now().UnixNano())

	attachedRes := Result[Typed]{shared: oc.res}
	attached, err := c.ensurePersistedHitValueLoaded(ctx, resolver, attachedRes)
	if err != nil {
		// Exhaustion cannot actually surface here: a cache-backed input
		// (shared.id != 0) returned above before reaching this flow, so
		// initCompletedResult saw a detached value, took its fresh branch,
		// and left oc.res realized — nothing remains to decode. Normalize
		// anyway so the sentinel cannot escape if that invariant ever moves.
		return nil, fmt.Errorf("attach dependency result: normalize attached result: %w", c.normalizeExhaustedResultError(ctx, oc.res, err))
	}
	attachedShared := attached.cacheSharedResult()
	if attachedShared == nil || attachedShared.id == 0 {
		return nil, fmt.Errorf("attach dependency result: attached result missing shared result ID")
	}
	return attached, nil
}

func (c *Cache) AddExplicitDependency(ctx context.Context, parent AnyResult, dep AnyResult, reason string) error {
	if parent == nil || dep == nil {
		return nil
	}

	parentShared := parent.cacheSharedResult()
	if parentShared == nil || parentShared.id == 0 {
		return fmt.Errorf("add explicit dependency: parent %T is not an attached result in this cache", parent)
	}
	depShared := dep.cacheSharedResult()
	if depShared == nil || depShared.id == 0 {
		return fmt.Errorf("add explicit dependency: dep %T is not an attached result in this cache", dep)
	}
	if parentShared.id == depShared.id {
		return nil
	}

	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()

	parentRes := c.resultsByID[parentShared.id]
	if parentRes == nil {
		return fmt.Errorf("add explicit dependency: parent result %d missing from cache", parentShared.id)
	}
	depRes := c.resultsByID[depShared.id]
	if depRes == nil {
		return fmt.Errorf("add explicit dependency: dep result %d missing from cache", depShared.id)
	}
	return c.addExplicitDependencyLocked(ctx, parentRes, depRes, reason)
}

func (c *Cache) addExplicitDependencyLocked(
	ctx context.Context,
	parentRes *sharedResult,
	depRes *sharedResult,
	reason string,
) error {
	if parentRes == nil || depRes == nil {
		return nil
	}
	if parentRes.id == depRes.id {
		return nil
	}
	if parentRes.deps == nil {
		parentRes.deps = make(map[sharedResultID]struct{})
	}
	if _, ok := parentRes.deps[depRes.id]; ok {
		return nil
	}

	parentRes.deps[depRes.id] = struct{}{}
	c.rememberDependencyEdgeLocked(parentRes, depRes)
	c.incrementIncomingOwnershipLocked(ctx, depRes)
	c.traceExplicitDepAdded(ctx, parentRes.id, depRes.id, reason)
	if err := c.recomputeRequiredSessionResourcesLocked(parentRes); err != nil {
		return err
	}

	return nil
}

func (c *Cache) rememberDependencyEdgeLocked(parentRes *sharedResult, depRes *sharedResult) {
	if parentRes == nil || depRes == nil || parentRes.id == 0 || depRes.id == 0 || parentRes.id == depRes.id {
		return
	}
	if depRes.depParents == nil {
		depRes.depParents = newSharedResultIDSet()
	}
	depRes.depParents.Insert(parentRes.id)
}

func (c *Cache) forgetDependencyEdgeLocked(parentID sharedResultID, depID sharedResultID) {
	depRes := c.resultsByID[depID]
	if depRes == nil || depRes.depParents == nil {
		return
	}
	depRes.depParents.Remove(parentID)
	if depRes.depParents.Empty() {
		depRes.depParents = nil
	}
}

type Result[T Typed] struct {
	// shared points at immutable payload + lifecycle state shared by all per-call Result values.
	shared *sharedResult

	// per-call cache-hit signal for callers/tests.
	hitCache bool

	// derefView means the result should present the dereferenced view of a
	// nullable/shared wrapper payload while keeping the same sharedResult.
	derefView bool

	// nullableWrapped means the result should present the same shared payload as
	// a nullable wrapper view while keeping the same sharedResult.
	nullableWrapped bool
}

var _ AnyResult = Result[Typed]{}

func (r Result[T]) Type() *ast.Type {
	state := r.shared.loadPayloadState()
	if r.shared == nil || state.self == nil {
		var zero T
		return zero.Type()
	}
	if r.nullableWrapped {
		var innerType *ast.Type
		if r.derefView {
			if inner, ok := derefTyped(state.self); ok && inner != nil {
				innerType = inner.Type()
			}
		} else {
			innerType = state.self.Type()
		}
		if innerType != nil {
			cp := *innerType
			cp.NonNull = false
			return &cp
		}
	}
	if r.derefView {
		if inner, ok := derefTyped(state.self); ok && inner != nil && inner.Type() != nil {
			cp := *inner.Type()
			cp.NonNull = true
			return &cp
		}
	}
	return state.self.Type()
}

// ID returns the runtime handle ID of the instance.
func (r Result[T]) ID() (*call.ID, error) {
	if r.shared == nil {
		return nil, fmt.Errorf("result has no shared payload")
	}
	if r.shared.id == 0 {
		return nil, fmt.Errorf("result %T is detached", r.Self())
	}
	typ := r.Type()
	if typ == nil {
		return nil, fmt.Errorf("result %T has no type", r.Self())
	}
	return call.NewEngineResultID(uint64(r.shared.id), call.NewType(typ)), nil
}

func (r Result[T]) RecipeID(ctx context.Context) (*call.ID, error) {
	call := r.shared.loadResultCall()
	if r.shared == nil || call == nil {
		return nil, fmt.Errorf("result %T has no call frame", r.Self())
	}
	c, err := EngineCache(ctx)
	if err != nil {
		return nil, err
	}
	return call.recipeID(ctx, c)
}

func (r Result[T]) RecipeDigest(ctx context.Context) (digest.Digest, error) {
	call := r.shared.loadResultCall()
	if r.shared == nil || call == nil {
		return "", fmt.Errorf("result %T has no call frame", r.Self())
	}
	c, err := EngineCache(ctx)
	if err != nil {
		return "", err
	}
	return call.deriveRecipeDigest(c)
}

func (r Result[T]) ContentPreferredDigest(ctx context.Context) (digest.Digest, error) {
	call := r.shared.loadResultCall()
	if r.shared == nil || call == nil {
		return "", fmt.Errorf("result %T has no call frame", r.Self())
	}
	c, err := EngineCache(ctx)
	if err != nil {
		return "", err
	}
	return call.deriveContentPreferredDigest(c)
}

func (r Result[T]) ResultCall() (*ResultCall, error) {
	call := r.shared.loadResultCall()
	if r.shared == nil || call == nil {
		return nil, fmt.Errorf("result %T has no call frame", r.Self())
	}
	return call.clone(), nil
}

func (r Result[T]) Self() T {
	self, ok := UnwrapAs[T](r.Unwrap())
	if !ok {
		var zero T
		return zero
	}
	return self
}

func (r Result[T]) SetField(field reflect.Value) error {
	return assign(field, r.Self())
}

// Unwrap returns the inner value of the instance.
func (r Result[T]) Unwrap() Typed {
	state := r.shared.loadPayloadState()
	if r.shared == nil {
		var zero T
		return zero
	}
	if state.self == nil {
		var zero T
		return zero
	}
	if r.nullableWrapped {
		wrapped := state.self
		if r.derefView {
			if inner, ok := derefTyped(state.self); ok && inner != nil {
				wrapped = inner
			}
		}
		return DynamicNullable{
			Elem:  wrapped,
			Value: wrapped,
			Valid: true,
		}
	}
	if r.derefView {
		if inner, ok := derefTyped(state.self); ok && inner != nil {
			return inner
		}
	}
	return state.self
}

func (r Result[T]) DerefValue() (AnyResult, bool) {
	state := r.shared.loadPayloadState()
	if r.derefView {
		return r, true
	}
	if r.nullableWrapped {
		r.nullableWrapped = false
		return r, true
	}
	if r.shared == nil || state.self == nil {
		return r, true
	}
	inner, valid := derefTyped(state.self)
	if !valid {
		if _, ok := any(state.self).(Derefable); ok {
			return nil, false
		}
		return r, true
	}
	if anyRes, ok := inner.(AnyResult); ok {
		return anyRes, true
	}
	return r.resultWithDerefView(), true
}

func (r Result[T]) NthValue(ctx context.Context, nth int) (AnyResult, error) {
	self := r.Self()
	enumerableSelf, ok := any(self).(Enumerable)
	if !ok {
		return nil, fmt.Errorf("cannot get %dth value from %T", nth, self)
	}
	parentCall := r.shared.loadResultCall()
	if r.shared == nil || parentCall == nil {
		return nil, fmt.Errorf("cannot get %dth value from %T without call frame", nth, self)
	}
	detached, err := enumerableSelf.NthValue(nth, parentCall)
	if err != nil || detached == nil {
		return detached, err
	}
	if r.shared.id == 0 {
		return detached, nil
	}

	childShared := detached.cacheSharedResult()
	if childShared != nil && childShared.id != 0 {
		srv := CurrentDagqlServer(ctx)
		if srv == nil {
			return nil, fmt.Errorf("load %dth value from %T: missing dagql server in context", nth, self)
		}
		clientMetadata, err := engine.ClientMetadataFromContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("load %dth value from %T: current client metadata: %w", nth, self, err)
		}
		if clientMetadata.SessionID == "" {
			return nil, fmt.Errorf("load %dth value from %T: empty session ID", nth, self)
		}
		cache, err := EngineCache(ctx)
		if err != nil {
			return nil, fmt.Errorf("load %dth value from %T: current dagql cache: %w", nth, self, err)
		}
		touchSharedResultLastUsed(childShared, time.Now().UnixNano())
		retResAny, err := wrapSharedResultWithResolver(ctx, childShared, true, srv)
		if err != nil {
			return nil, fmt.Errorf("load %dth value from %T: reconstruct result: %w", nth, self, err)
		}
		cache.trackSessionResult(ctx, clientMetadata.SessionID, retResAny, true)
		return retResAny, nil
	}

	srv := CurrentDagqlServer(ctx)
	if srv == nil {
		return nil, fmt.Errorf("load %dth value from %T: missing dagql server in context", nth, self)
	}
	if parentCall.Type == nil || parentCall.Type.Elem == nil {
		return nil, fmt.Errorf("cannot get %dth value from %T without element type", nth, self)
	}
	req := &CallRequest{
		ResultCall: parentCall.fork(),
	}
	req.Type = req.Type.Elem.clone()
	req.Receiver = &ResultCallRef{ResultID: uint64(r.shared.id), shared: r.shared}
	req.Nth = int64(nth)
	if shared := detached.cacheSharedResult(); shared != nil && shared.id == 0 {
		shared.storeResultCall(req.ResultCall.clone())
	}
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("load %dth value from %T: current client metadata: %w", nth, self, err)
	}
	if clientMetadata.SessionID == "" {
		return nil, fmt.Errorf("load %dth value from %T: empty session ID", nth, self)
	}
	cache, err := EngineCache(ctx)
	if err != nil {
		return nil, fmt.Errorf("load %dth value from %T: current dagql cache: %w", nth, self, err)
	}
	return cache.GetOrInitCall(ctx, clientMetadata.SessionID, srv, req, func(context.Context) (AnyResult, error) {
		return detached, nil
	})
}

func (r Result[T]) resultWithDerefView() Result[T] {
	r.derefView = true
	r.nullableWrapped = false
	return r
}

func (r Result[T]) withDerefViewAny() AnyResult {
	return r.resultWithDerefView()
}

func (r Result[T]) resultNullableWrapped() Result[T] {
	r.nullableWrapped = true
	return r
}

func (r Result[T]) NullableWrapped() AnyResult {
	return r.resultNullableWrapped()
}

func derefTyped(val Typed) (Typed, bool) {
	derefable, ok := any(val).(Derefable)
	if !ok {
		return nil, false
	}
	return derefable.Deref()
}

func (r Result[T]) WithContentDigest(ctx context.Context, contentDigest digest.Digest) (Result[T], error) {
	if contentDigest == "" {
		return r, fmt.Errorf("set content digest on %T: empty digest", r.Self())
	}
	if r.shared == nil {
		return r, fmt.Errorf("set content digest on %T: missing shared result", r.Self())
	}
	if r.shared.id != 0 {
		cache, err := EngineCache(ctx)
		if err != nil {
			return r, fmt.Errorf("set content digest on %T: current dagql cache: %w", r.Self(), err)
		}
		if err := cache.TeachContentDigest(ctx, r, contentDigest); err != nil {
			return r, err
		}
		return r, nil
	}

	state := r.shared.loadPayloadState()
	frame := r.shared.loadResultCall()
	if frame == nil {
		return r, fmt.Errorf("set content digest on %T: missing call frame", r.Self())
	}
	var deps map[sharedResultID]struct{}
	if len(r.shared.deps) > 0 {
		deps = make(map[sharedResultID]struct{}, len(r.shared.deps))
		for depID := range r.shared.deps {
			deps[depID] = struct{}{}
		}
	}
	r.shared = &sharedResult{
		self:                  state.self,
		isObject:              state.isObject,
		objClass:              state.objClass,
		resultCall:            frame.fork(),
		deps:                  deps,
		sessionResourceHandle: r.shared.sessionResourceHandle,
		requiredSessionResources: func() *set.TreeSet[SessionResourceHandle] {
			if r.shared.requiredSessionResources == nil {
				return nil
			}
			return r.shared.requiredSessionResources.Copy()
		}(),
		materialization:    r.shared.cloneMaterializationState(),
		createdAtUnixNano:  state.createdAtUnixNano,
		lastUsedAtUnixNano: state.lastUsedAtUnixNano,
		cacheUsageSizeByIdentity: func() map[string]int64 {
			if len(r.shared.cacheUsageSizeByIdentity) == 0 {
				return nil
			}
			cp := make(map[string]int64, len(r.shared.cacheUsageSizeByIdentity))
			for id, sz := range r.shared.cacheUsageSizeByIdentity {
				cp[id] = sz
			}
			return cp
		}(),
		cacheUsageRecordTypeByID: func() map[string]string {
			if len(r.shared.cacheUsageRecordTypeByID) == 0 {
				return nil
			}
			cp := make(map[string]string, len(r.shared.cacheUsageRecordTypeByID))
			for id, recordType := range r.shared.cacheUsageRecordTypeByID {
				cp[id] = recordType
			}
			return cp
		}(),
		description: r.shared.description,
		recordType:  r.shared.recordType,
	}
	frame = r.shared.loadResultCall()
	replaced := false
	for i, extra := range frame.ExtraDigests {
		if extra.Label != call.ExtraDigestLabelContent {
			continue
		}
		frame.ExtraDigests[i].Digest = contentDigest
		replaced = true
		break
	}
	if !replaced {
		frame.ExtraDigests = append(frame.ExtraDigests, call.ExtraDigest{
			Label:  call.ExtraDigestLabelContent,
			Digest: contentDigest,
		})
	}
	return r, nil
}

func (r Result[T]) WithSessionResourceHandle(ctx context.Context, handle SessionResourceHandle) (Result[T], error) {
	if handle == "" {
		return r, fmt.Errorf("set session resource handle on %T: empty handle", r.Self())
	}
	if r.shared == nil {
		return r, fmt.Errorf("set session resource handle on %T: missing shared result", r.Self())
	}
	if r.shared.id != 0 {
		cache, err := EngineCache(ctx)
		if err != nil {
			return r, fmt.Errorf("set session resource handle on %T: current dagql cache: %w", r.Self(), err)
		}
		cache.egraphMu.Lock()
		defer cache.egraphMu.Unlock()

		cached := cache.resultsByID[r.shared.id]
		if cached == nil {
			return r, fmt.Errorf("set session resource handle on %T: result %d missing from cache", r.Self(), r.shared.id)
		}
		cached.sessionResourceHandle = handle
		if err := cache.recomputeRequiredSessionResourcesLocked(cached); err != nil {
			return r, err
		}
		return r, nil
	}

	state := r.shared.loadPayloadState()
	frame := r.shared.loadResultCall()
	var deps map[sharedResultID]struct{}
	if len(r.shared.deps) > 0 {
		deps = make(map[sharedResultID]struct{}, len(r.shared.deps))
		for depID := range r.shared.deps {
			deps[depID] = struct{}{}
		}
	}
	reqs := set.NewTreeSet(compareSessionResourceHandles)
	if r.shared.requiredSessionResources != nil {
		reqs = r.shared.requiredSessionResources.Copy()
	}
	reqs.Insert(handle)
	r.shared = &sharedResult{
		self:                     state.self,
		isObject:                 state.isObject,
		objClass:                 state.objClass,
		resultCall:               frame,
		deps:                     deps,
		sessionResourceHandle:    handle,
		requiredSessionResources: reqs,
		materialization:          r.shared.cloneMaterializationState(),
		createdAtUnixNano:        state.createdAtUnixNano,
		lastUsedAtUnixNano:       state.lastUsedAtUnixNano,
		cacheUsageSizeByIdentity: func() map[string]int64 {
			if len(r.shared.cacheUsageSizeByIdentity) == 0 {
				return nil
			}
			cp := make(map[string]int64, len(r.shared.cacheUsageSizeByIdentity))
			for id, sz := range r.shared.cacheUsageSizeByIdentity {
				cp[id] = sz
			}
			return cp
		}(),
		cacheUsageRecordTypeByID: func() map[string]string {
			if len(r.shared.cacheUsageRecordTypeByID) == 0 {
				return nil
			}
			cp := make(map[string]string, len(r.shared.cacheUsageRecordTypeByID))
			for id, recordType := range r.shared.cacheUsageRecordTypeByID {
				cp[id] = recordType
			}
			return cp
		}(),
		description: r.shared.description,
		recordType:  r.shared.recordType,
	}
	if frame != nil {
		r.shared.storeResultCall(frame.fork())
	}
	return r, nil
}

// WithContentDigestAny is WithContentDigest but returns an AnyResult, required
// for polymorphic code paths like module function call plumbing.
func (r Result[T]) WithContentDigestAny(ctx context.Context, customDigest digest.Digest) (AnyResult, error) {
	return r.WithContentDigest(ctx, customDigest)
}

func (r Result[T]) WithSessionResourceHandleAny(ctx context.Context, handle SessionResourceHandle) (AnyResult, error) {
	return r.WithSessionResourceHandle(ctx, handle)
}

// String returns the instance in Class@sha256:... format.
func (r Result[T]) String() string {
	typ := r.Type()
	if typ == nil {
		return "<nil>@<nil>"
	}
	id, err := r.ID()
	if err != nil {
		return fmt.Sprintf("%s@<detached>", typ.Name())
	}
	enc, err := id.Encode()
	if err != nil {
		return fmt.Sprintf("%s@<encode-error>", typ.Name())
	}
	return fmt.Sprintf("%s@%s", typ.Name(), enc)
}

func (r Result[T]) MarshalJSON() ([]byte, error) {
	id, err := r.ID()
	if err != nil {
		return nil, err
	}
	return json.Marshal(id)
}

func (r Result[T]) HitCache() bool {
	return r.hitCache
}

func (r Result[T]) cacheSharedResult() *sharedResult {
	return r.shared
}

type ObjectResult[T Typed] struct {
	Result[T]
	class Class[T]
}

var _ AnyObjectResult = ObjectResult[Typed]{}

func (r ObjectResult[T]) MarshalJSON() ([]byte, error) {
	return r.Result.MarshalJSON()
}

func (r ObjectResult[T]) DerefValue() (AnyResult, bool) {
	state := r.shared.loadPayloadState()
	if r.derefView {
		return r, true
	}
	if r.shared == nil || state.self == nil {
		return r, true
	}
	inner, valid := derefTyped(state.self)
	if !valid {
		if _, ok := any(state.self).(Derefable); ok {
			return nil, false
		}
		return r, true
	}
	if anyRes, ok := inner.(AnyResult); ok {
		return anyRes, true
	}
	r.Result = r.Result.resultWithDerefView()
	return r, true
}

func (r ObjectResult[T]) SetField(field reflect.Value) error {
	return assign(field, r.Result)
}

// ObjectType returns the ObjectType of the instance.
func (r ObjectResult[T]) ObjectType() ObjectType {
	return r.class
}

func (r ObjectResult[T]) Receiver(ctx context.Context, srv *Server) (AnyObjectResult, error) {
	if srv == nil {
		return nil, fmt.Errorf("receiver: server is nil")
	}
	ctx = srvToContext(ctx, srv)
	call, err := r.ResultCall()
	if err != nil {
		return nil, err
	}
	if call.Receiver == nil {
		return nil, nil
	}
	if call.Receiver.ResultID == 0 {
		return nil, fmt.Errorf("receiver: result is detached")
	}
	cache, err := EngineCache(srvToContext(ctx, srv))
	if err != nil {
		return nil, fmt.Errorf("receiver: current dagql cache: %w", err)
	}
	clientMetadata, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("receiver: current client metadata: %w", err)
	}
	if clientMetadata.SessionID == "" {
		return nil, fmt.Errorf("receiver: empty session ID")
	}
	res, err := cache.loadResultByResultID(ctx, clientMetadata.SessionID, srv, call.Receiver.ResultID)
	if err != nil {
		return nil, fmt.Errorf("receiver: load result %d: %w", call.Receiver.ResultID, err)
	}
	obj, ok := res.(AnyObjectResult)
	if !ok {
		return nil, fmt.Errorf("receiver: result %d is %T, not object result", call.Receiver.ResultID, res)
	}
	return obj, nil
}

func (r ObjectResult[T]) WithContentDigest(ctx context.Context, contentDigest digest.Digest) (ObjectResult[T], error) {
	res, err := r.Result.WithContentDigest(ctx, contentDigest)
	if err != nil {
		return ObjectResult[T]{}, err
	}
	return ObjectResult[T]{
		Result: res,
		class:  r.class,
	}, nil
}

func (r ObjectResult[T]) WithSessionResourceHandle(ctx context.Context, handle SessionResourceHandle) (ObjectResult[T], error) {
	res, err := r.Result.WithSessionResourceHandle(ctx, handle)
	if err != nil {
		return ObjectResult[T]{}, err
	}
	return ObjectResult[T]{
		Result: res,
		class:  r.class,
	}, nil
}

// WithContentDigestAny is WithContentDigest but returns an AnyResult, required
// for polymorphic code paths like module function call plumbing.
func (r ObjectResult[T]) WithContentDigestAny(ctx context.Context, customDigest digest.Digest) (AnyResult, error) {
	res, err := r.Result.WithContentDigest(ctx, customDigest)
	if err != nil {
		return nil, err
	}
	return ObjectResult[T]{
		Result: res,
		class:  r.class,
	}, nil
}

func (r ObjectResult[T]) WithSessionResourceHandleAny(ctx context.Context, handle SessionResourceHandle) (AnyResult, error) {
	res, err := r.Result.WithSessionResourceHandle(ctx, handle)
	if err != nil {
		return nil, err
	}
	return ObjectResult[T]{
		Result: res,
		class:  r.class,
	}, nil
}

func (r ObjectResult[T]) objectResultWithDerefView() AnyResult {
	r.Result = r.Result.resultWithDerefView()
	return r
}

func (r ObjectResult[T]) withDerefViewAny() AnyResult {
	return r.objectResultWithDerefView()
}

func (r ObjectResult[T]) NullableWrapped() AnyResult {
	return r.Result.resultNullableWrapped()
}

func (r ObjectResult[T]) cacheSharedResult() *sharedResult {
	return r.shared
}

type cacheContextKey struct {
	key string
}

type lazyEvalStackCtxKey struct{}
type lazyEvalStackNode struct {
	id     sharedResultID
	parent *lazyEvalStackNode
}

func lazyEvalFuncOfResult(val AnyResult) LazyEvalFunc {
	if val == nil {
		return nil
	}
	lazy, ok := UnwrapAs[HasLazyEvaluation](val)
	if !ok {
		return nil
	}
	return lazy.LazyEvalFunc()
}

// markPendingWorkFromRestore records that a restored result's deferred work
// was re-attached from its persisted lazy fragment. A permanent failure of
// that work is then retained-source exhaustion — the row drops so future
// lookups heal — where a live call's own deferred work failing stays that
// call's failure.
func (c *Cache) markPendingWorkFromRestore(shared *sharedResult, val AnyResult) {
	if shared == nil || !shared.restored || val == nil {
		return
	}
	if lazyEvalFuncOfResult(val) == nil {
		return
	}
	shared.materializeMu.Lock()
	if !shared.lazyEvalComplete {
		shared.pendingWorkFromRestore = true
	}
	shared.materializeMu.Unlock()
}

func (c *Cache) registerLazyEvaluation(shared *sharedResult, val AnyResult) {
	if shared == nil || val == nil {
		return
	}
	lazyEval := lazyEvalFuncOfResult(val)
	if lazyEval == nil {
		return
	}

	shared.materializeMu.Lock()
	if shared.lazyEval == nil && !shared.lazyEvalComplete {
		shared.lazyEval = lazyEval
	}
	shared.materializeMu.Unlock()
}

func lazyEvalStackFromContext(ctx context.Context) *lazyEvalStackNode {
	stack, _ := ctx.Value(lazyEvalStackCtxKey{}).(*lazyEvalStackNode)
	return stack
}

func lazyEvalStackContains(stack *lazyEvalStackNode, id sharedResultID) bool {
	for cur := stack; cur != nil; cur = cur.parent {
		if cur.id == id {
			return true
		}
	}
	return false
}

type resumedCallbackSpan struct {
	trace.Span
	sc trace.SpanContext
	tp trace.TracerProvider
}

func (s resumedCallbackSpan) SpanContext() trace.SpanContext {
	return s.sc
}

func (s resumedCallbackSpan) TracerProvider() trace.TracerProvider {
	return s.tp
}

// waitForMaterialization is the waiter side of the materialization
// protocol, shared by both phases (restored-value decode and deferred lazy
// work): waiters are counted, a departing waiter that leaves the runner
// with no audience cancels it with its own cause, and the last waiter to
// observe a finished attempt clears the protocol state so the next demand
// can retry.
func (c *Cache) waitForMaterialization(ctx context.Context, shared *sharedResult, waitCh chan struct{}) error {
	var waitErr error
	select {
	case <-waitCh:
		shared.materializeMu.Lock()
		waitErr = shared.materializeErr
		shared.materializeWaiters--
		if shared.materializeWaiters == 0 && shared.materializeWaitCh == waitCh {
			shared.materializeWaitCh = nil
			shared.materializeCancel = nil
			shared.materializeErr = nil
		}
		shared.materializeMu.Unlock()
		// Tag the failure with the result it belongs to so that an enclosing
		// lazy callback's resume span can tell "a prerequisite failed" apart
		// from "my own deferred work failed". See blockedOnPrerequisite.
		if waitErr != nil {
			waitErr = &prerequisiteEvalError{err: waitErr, resultID: shared.id}
		}
	case <-ctx.Done():
		waitErr = context.Cause(ctx)
		shared.materializeMu.Lock()
		shared.materializeWaiters--
		lastWaiter := shared.materializeWaiters == 0
		cancel := shared.materializeCancel
		shared.materializeMu.Unlock()
		if lastWaiter && cancel != nil {
			cancel(waitErr)
		}
	}
	return waitErr
}

// prerequisiteEvalError wraps a lazy-evaluation failure with the identity of
// the result whose evaluation failed. It does not change the error message;
// it only carries provenance so enclosing evaluations can classify cascaded
// failures without forcing prerequisite evaluation order.
type prerequisiteEvalError struct {
	err      error
	resultID sharedResultID
}

func (e *prerequisiteEvalError) Error() string { return e.err.Error() }
func (e *prerequisiteEvalError) Unwrap() error { return e.err }

// blockedOnPrerequisite reports whether err originated from evaluating a
// result other than selfID — i.e. the current result's lazy callback was
// blocked by a failing prerequisite rather than failing its own work.
func blockedOnPrerequisite(err error, selfID sharedResultID) bool {
	var prereq *prerequisiteEvalError
	if !errors.As(err, &prereq) {
		return false
	}
	return prereq.resultID != selfID
}

func (c *Cache) Evaluate(ctx context.Context, results ...AnyResult) error {
	switch len(results) {
	case 0:
		return nil
	case 1:
		return c.evaluateOne(ctx, results[0])
	}

	eg, egCtx := errgroup.WithContext(ctx)
	for _, res := range results {
		res := res
		eg.Go(func() error {
			return c.evaluateOne(egCtx, res)
		})
	}
	return eg.Wait()
}

func (c *Cache) evaluateOne(ctx context.Context, res AnyResult) error {
	if c == nil {
		return errors.New("evaluate: nil cache")
	}
	if res == nil {
		return nil
	}
	shared := res.cacheSharedResult()
	if shared == nil || shared.id == 0 {
		return fmt.Errorf("evaluate %T: detached result", res)
	}

	stack := lazyEvalStackFromContext(ctx)
	if stack != nil {
		if lazyEvalStackContains(stack, shared.id) {
			return fmt.Errorf("recursive lazy evaluation detected")
		}
	}

	stackCtx := context.WithValue(ctx, lazyEvalStackCtxKey{}, &lazyEvalStackNode{
		id:     shared.id,
		parent: stack,
	})

	// Fast path: if evaluation is already complete or there is nothing to do,
	// skip preflight entirely.
	shared.materializeMu.Lock()
	if shared.lazyEvalComplete || lazyEvalFuncOfResult(res) == nil {
		shared.materializeMu.Unlock()
		return nil
	}
	shared.materializeMu.Unlock()

	shared.materializeMu.Lock()
	currentLazyEval := lazyEvalFuncOfResult(res)
	if currentLazyEval == nil {
		shared.lazyEval = nil
		shared.lazyEvalComplete = true
		shared.materializeMu.Unlock()
		return nil
	}
	if shared.lazyEvalComplete {
		shared.materializeMu.Unlock()
		return nil
	}
	shared.lazyEval = currentLazyEval
	if shared.lazyEval == nil {
		shared.materializeMu.Unlock()
		return nil
	}
	if shared.materializeWaitCh != nil {
		waitCh := shared.materializeWaitCh
		shared.materializeWaiters++
		shared.materializeMu.Unlock()
		return c.waitForMaterialization(stackCtx, shared, waitCh)
	}

	waitCh := make(chan struct{})
	evalCtx, cancel := context.WithCancelCause(context.WithoutCancel(stackCtx))
	lazyEval := shared.lazyEval
	resultCall := shared.loadResultCall()
	if resultCall != nil {
		evalCtx = ContextWithCall(evalCtx, resultCall)
	}
	shared.materializeWaitCh = waitCh
	shared.materializeCancel = cancel
	shared.materializeWaiters = 1
	shared.materializeErr = nil
	shared.materializeMu.Unlock()

	go c.runDeferredWork(evalCtx, shared, resultCall, lazyEval, waitCh)

	return c.waitForMaterialization(stackCtx, shared, waitCh)
}

// runDeferredWork is the deferred-work phase of the materialization
// protocol: it runs the result's pending lazy work on a caller-detached
// context with resume-span attribution, classifies a permanent failure of
// restored work as retained-source exhaustion (dropping the result), and
// completes the wait protocol for every current waiter.
func (c *Cache) runDeferredWork(evalCtx context.Context, shared *sharedResult, resultCall *ResultCall, lazyEval LazyEvalFunc, waitCh chan struct{}) {
	{
		callbackCtx := evalCtx
		var resumeSpan trace.Span
		if clientMD, err := engine.ClientMetadataFromContext(evalCtx); err == nil && clientMD.SessionID != "" {
			if originalSpanCtx, ok := c.sessionLazySpanContext(clientMD.SessionID, shared.id); ok {
				spanName := "resume lazy evaluation"
				if resultCall != nil && resultCall.Field != "" {
					spanName = "resume " + resultCall.Field
				}
				// Lazy failure attribution: link the resume span back to all
				// API spans that installed/own this result in the session.
				// dagui interprets cause-purpose links as "this resume is the
				// cause of those installs failing" and propagates failure.
				installCtxs := c.sessionResultInstallSpanContexts(clientMD.SessionID, shared.id)
				links := lazyResumeLinks(originalSpanCtx, installCtxs)
				var resumeCtx context.Context
				resumeCtx, resumeSpan = Tracer(evalCtx).Start(
					evalCtx,
					spanName,
					trace.WithLinks(links...),
					telemetry.Passthrough(),
				)
				callbackCtx = trace.ContextWithSpan(resumeCtx, resumedCallbackSpan{
					Span: resumeSpan,
					sc:   originalSpanCtx,
					tp:   resumeSpan.TracerProvider(),
				})
			}
		}

		var err error
		// End resumeSpan before close(waitCh) so that callers awaiting
		// evaluation observe the span as ended (and exported, via sync
		// processors). Deferring would fire only after close(waitCh) and
		// race with the caller's flush/read of exported spans.
		runEval := func() {
			if resumeSpan != nil {
				defer func() {
					// If the callback failed only because a prerequisite
					// result's evaluation failed, this result's own deferred
					// work never ran. Mark the resume span blocked so the UI
					// returns the owning API spans to pending instead of
					// marking them caused-failed with the cascaded error. The
					// failing prerequisite's own resume span carries the real
					// failure and its install-span cause links.
					if err != nil && blockedOnPrerequisite(err, shared.id) {
						resumeSpan.SetAttributes(attribute.Bool(telemetryattrs.DagBlockedAttr, true))
					}
					telemetry.EndWithCause(resumeSpan, &err)
				}()
			}

			leaseCtx, release, leaseErr := withOperationLease(withoutOperationLease(callbackCtx))
			if leaseErr != nil {
				err = fmt.Errorf("acquire operation lease: %w", leaseErr)
				return
			}
			callbackCtx = leaseCtx

			err = lazyEval(callbackCtx)
			if err == nil {
				err = c.syncResultSnapshotLeases(callbackCtx, shared)
			}
			if releaseErr := release(context.WithoutCancel(callbackCtx)); releaseErr != nil && err == nil {
				err = releaseErr
			}
		}
		runEval()

		shared.materializeMu.Lock()
		restoredWork := shared.pendingWorkFromRestore
		shared.materializeMu.Unlock()
		if err != nil && restoredWork && !isTransientMaterializeFailure(err) {
			// The restored fragment cannot re-make this content, and decode
			// already preferred a snapshot when one was viable, so nothing
			// remains: exhaustion is terminal for the result, not just the
			// attempt. Drop it — with its dependents — so future lookups
			// miss and heal; current waiters receive the terminal outcome.
			dropErr := c.dropExhaustedResult(callbackCtx, shared)
			if dropErr != nil {
				err = errors.Join(err, dropErr)
			}
			// The waiters get an honest terminal error, not the internal
			// exhaustion sentinel: deferred-work failures surface through
			// Evaluate, which has no demote consumer, and the drop above
			// already did everything the sentinel would ask for.
			err = fmt.Errorf("restored result %d's deferred work failed permanently and the result was dropped from the cache; retrying will re-execute it: %w", shared.id, err)
		}

		shared.materializeMu.Lock()
		shared.materializeErr = err
		if err == nil {
			shared.lazyEvalComplete = true
			shared.lazyEval = nil
		}
		clearState := shared.materializeWaiters == 0 && shared.materializeWaitCh == waitCh
		if clearState {
			shared.materializeWaitCh = nil
			shared.materializeCancel = nil
			shared.materializeErr = nil
		}
		shared.materializeMu.Unlock()

		close(waitCh)
	}
}

func (c *Cache) Close(ctx context.Context) error {
	c.closeOnce.Do(func() {
		slog.Info(
			"starting dagql cache close",
			"hasSQLDB", c.sqlDB != nil,
			"hasPersistDB", c.pdb != nil,
		)
		if err := c.persistCurrentState(ctx); err != nil {
			slog.Error("failed to persist dagql cache during close", "err", err)
			c.closeErr = errors.Join(c.closeErr, err)
		}
		if c.closeErr != nil {
			if closeErr := closeCacheDBs(c.sqlDB, c.pdb); closeErr != nil {
				slog.Error("failed to close dagql persistence databases after cache close error", "err", closeErr)
				c.closeErr = errors.Join(c.closeErr, closeErr)
			}
			c.sqlDB = nil
			c.pdb = nil
			slog.Error("dagql cache close exiting with error", "err", c.closeErr)
			return
		}
		c.writeServeStatsFile()
		if c.pdb != nil {
			slog.Info("marking dagql cache clean shutdown")
			if err := c.pdb.UpsertMeta(ctx, persistdb.MetaKeyCleanShutdown, "1"); err != nil {
				slog.Warn("failed to mark clean shutdown in persistence metadata", "err", err)
			}
			slog.Warn("successfully marked clean shutdown in persistence metadata")
		}
		if closeErr := closeCacheDBs(c.sqlDB, c.pdb); closeErr != nil {
			slog.Error("failed to close dagql persistence databases", "err", closeErr)
			c.closeErr = closeErr
		}
		c.sqlDB = nil
		c.pdb = nil
		slog.Info("completed dagql cache close successfully")
	})
	return c.closeErr
}

func (c *Cache) CloseDiscardingPersistence() error {
	c.closeOnce.Do(func() {
		slog.Info(
			"discarding dagql cache without persistence",
			"hasSQLDB", c.sqlDB != nil,
			"hasPersistDB", c.pdb != nil,
		)
		if closeErr := closeCacheDBs(c.sqlDB, c.pdb); closeErr != nil {
			slog.Error("failed to close discarded dagql persistence databases", "err", closeErr)
			c.closeErr = errors.Join(c.closeErr, closeErr)
		}
		c.sqlDB = nil
		c.pdb = nil
	})
	return c.closeErr
}

func (c *Cache) PersistenceResetReason() CachePersistenceResetReason {
	if c == nil {
		return CachePersistenceResetNone
	}
	return c.persistenceResetReason
}

func (c *Cache) Size() int {
	c.callsMu.Lock()
	ongoingCalls := len(c.ongoingCalls)
	ongoingArbitrary := len(c.ongoingArbitraryCalls)
	completedArbitrary := len(c.completedArbitraryCalls)
	c.callsMu.Unlock()

	c.egraphMu.RLock()
	completedCalls := len(c.resultOutputEqClasses)
	c.egraphMu.RUnlock()

	// TODO: Re-implement size accounting directly from egraph state instead of
	// relying on mixed index-oriented counters.
	total := ongoingCalls
	total += completedCalls
	total += ongoingArbitrary
	total += completedArbitrary
	return total
}

func (c *Cache) EntryStats() CacheEntryStats {
	c.callsMu.Lock()
	stats := CacheEntryStats{
		OngoingCalls:       len(c.ongoingCalls),
		OngoingArbitrary:   len(c.ongoingArbitraryCalls),
		CompletedArbitrary: len(c.completedArbitraryCalls),
	}
	c.callsMu.Unlock()

	c.egraphMu.RLock()
	stats.CompletedCalls = len(c.resultOutputEqClasses)
	stats.RetainedCalls = len(c.persistedEdgesByResult)
	c.egraphMu.RUnlock()

	return stats
}

func (c *Cache) UsageEntriesAll(ctx context.Context) []CacheUsageEntry {
	activeRoots := c.snapshotSessionResultIDs()
	c.measureAllResultSizes(ctx)
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	entries := c.usageEntriesLocked(activeRoots)
	return entries
}

func (c *Cache) usageEntriesLocked(activeRoots map[sharedResultID]struct{}) []CacheUsageEntry {
	entries := make([]CacheUsageEntry, 0, len(c.resultsByID))
	for resID, res := range c.resultsByID {
		if res == nil {
			continue
		}
		_, activelyUsed := activeRoots[resID]
		state := res.loadPayloadState()
		createdAt := state.createdAtUnixNano
		lastUsedAt := state.lastUsedAtUnixNano
		if createdAt == 0 {
			createdAt = lastUsedAt
		}
		if lastUsedAt == 0 {
			lastUsedAt = createdAt
		}
		recordTypes := cacheUsageRecordTypesFromMap(res.cacheUsageRecordTypeByID)
		recordType := cacheUsagePrimaryRecordType(recordTypes, res.recordType)
		dagqlCall := c.cacheUsageDagqlCallLocked(res)
		description := res.description
		if description == "" {
			description = fmt.Sprintf("dagql cache result %d", resID)
		}
		sizeBytes := int64(0)
		for _, sz := range res.cacheUsageSizeByIdentity {
			if sz > 0 {
				sizeBytes += sz
			}
		}
		entries = append(entries, CacheUsageEntry{
			ID:                        fmt.Sprintf("dagql.result.%d", resID),
			Description:               description,
			RecordType:                recordType,
			RecordTypes:               recordTypes,
			DagqlCall:                 dagqlCall,
			SizeBytes:                 sizeBytes,
			CreatedTimeUnixNano:       createdAt,
			MostRecentUseTimeUnixNano: lastUsedAt,
			ActivelyUsed:              activelyUsed,
		})
	}

	slices.SortFunc(entries, func(a, b CacheUsageEntry) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return entries
}

func (c *Cache) cacheUsageDagqlCallLocked(res *sharedResult) string {
	if c == nil || res == nil {
		return ""
	}
	frame := res.loadResultCall()
	if frame == nil {
		return ""
	}

	fieldName := ""
	if identityField, err := resultCallIdentityField(frame); err == nil {
		fieldName = identityField
	}

	receiverTypeName := ""
	if frame.Receiver != nil {
		if receiverRes := c.resultsByID[sharedResultID(frame.Receiver.ResultID)]; receiverRes != nil {
			receiverFrame := receiverRes.loadResultCall()
			switch {
			case receiverFrame != nil && receiverFrame.Type != nil && receiverFrame.Type.NamedType != "":
				receiverTypeName = receiverFrame.Type.NamedType
			default:
				receiverState := receiverRes.loadPayloadState()
				receiverTypeName = sharedResultObjectTypeName(receiverRes, receiverState)
			}
		}
	}

	switch {
	case receiverTypeName != "" && fieldName != "":
		return receiverTypeName + "." + fieldName
	case fieldName != "":
		if frame.Kind == ResultCallKindField {
			return "Query." + fieldName
		}
		return fieldName
	default:
		return ""
	}
}

func cacheUsageRecordTypesFromMap(recordTypeByID map[string]string) []string {
	if len(recordTypeByID) == 0 {
		return nil
	}
	recordTypes := make([]string, 0, len(recordTypeByID))
	seen := make(map[string]struct{}, len(recordTypeByID))
	for _, recordType := range recordTypeByID {
		if recordType == "" {
			continue
		}
		if _, ok := seen[recordType]; ok {
			continue
		}
		seen[recordType] = struct{}{}
		recordTypes = append(recordTypes, recordType)
	}
	slices.Sort(recordTypes)
	return recordTypes
}

func cacheUsagePrimaryRecordType(recordTypes []string, fallback string) string {
	switch len(recordTypes) {
	case 0:
		if fallback != "" {
			return fallback
		}
		return "dagql.unknown"
	case 1:
		return recordTypes[0]
	default:
		return "mixed"
	}
}

type cacheUsageMeasurementInput struct {
	resultID         sharedResultID
	self             Typed
	snapshotLinks    []PersistedSnapshotRefLink
	identities       []string
	existingSizeByID map[string]int64
	sizeMayChange    bool
}

type cacheUsageIdentityMeasurement struct {
	sizeBytes  int64
	recordType string
}

func (c *Cache) measureAllResultSizes(ctx context.Context) {
	inputs := c.collectUsageMeasurementInputs()
	if len(inputs) == 0 {
		return
	}
	measurements := buildCacheUsageMeasurements(ctx, c.snapshotManager, inputs)
	c.publishUsageMeasurements(measurements)
}

func (c *Cache) collectUsageMeasurementInputs() []cacheUsageMeasurementInput {
	c.egraphMu.RLock()
	defer c.egraphMu.RUnlock()
	inputs := make([]cacheUsageMeasurementInput, 0, len(c.resultsByID))
	for resID, res := range c.resultsByID {
		if res == nil {
			continue
		}
		state := res.loadPayloadState()
		var (
			self          Typed
			snapshotLinks []PersistedSnapshotRefLink
			identities    []string
			sizeMayChange bool
		)
		if state.realized && state.self != nil {
			self = state.self
			identities = cacheUsageIdentitiesFromSelf(state.self)
			sizeMayChange = cacheUsageSizeMayChangeFromSelf(state.self)
		} else {
			snapshotLinks = slices.Clone(state.snapshotOwnerLinks)
			identities = cacheUsageIdentitiesFromSnapshotLinks(snapshotLinks)
		}
		if len(identities) == 0 {
			continue
		}
		existing := make(map[string]int64, len(res.cacheUsageSizeByIdentity))
		for identity, sizeBytes := range res.cacheUsageSizeByIdentity {
			existing[identity] = sizeBytes
		}
		inputs = append(inputs, cacheUsageMeasurementInput{
			resultID:         resID,
			self:             self,
			snapshotLinks:    snapshotLinks,
			identities:       identities,
			existingSizeByID: existing,
			sizeMayChange:    sizeMayChange,
		})
	}
	return inputs
}

func buildCacheUsageMeasurements(ctx context.Context, snapshotManager bkcache.SnapshotManager, inputs []cacheUsageMeasurementInput) map[sharedResultID]map[string]cacheUsageIdentityMeasurement {
	if len(inputs) == 0 {
		return nil
	}

	inputByResultID := make(map[sharedResultID]cacheUsageMeasurementInput, len(inputs))
	ownerByIdentity := make(map[string]sharedResultID)
	for _, input := range inputs {
		inputByResultID[input.resultID] = input
		for _, identity := range input.identities {
			cur := ownerByIdentity[identity]
			if cur == 0 || input.resultID < cur {
				ownerByIdentity[identity] = input.resultID
			}
		}
	}

	identities := make([]string, 0, len(ownerByIdentity))
	for identity := range ownerByIdentity {
		identities = append(identities, identity)
	}
	slices.Sort(identities)

	measurementByIdentity := make(map[string]cacheUsageIdentityMeasurement, len(ownerByIdentity))
	for _, identity := range identities {
		ownerID := ownerByIdentity[identity]
		input := inputByResultID[ownerID]
		var (
			sizeBytes int64
			ok        bool
			err       error
		)
		if !input.sizeMayChange {
			if existingSizeBytes, found := input.existingSizeByID[identity]; found {
				sizeBytes = existingSizeBytes
				ok = true
			}
		}

		if !ok {
			if input.self != nil {
				sizeBytes, ok, err = cacheUsageSizeBytesFromSelf(ctx, snapshotManager, input.self, identity)
			} else if len(input.snapshotLinks) > 0 {
				sizeBytes, ok, err = cacheUsageSizeBytesFromSnapshotLink(ctx, snapshotManager, identity)
			}
			if err != nil {
				slog.Warn("failed to determine cache usage size",
					"resultID", ownerID,
					"usageIdentity", identity,
					"err", err)
				continue
			}
			if !ok {
				continue
			}
		}

		recordType, _, err := cacheUsageRecordTypeFromSnapshotMetadata(ctx, snapshotManager, identity)
		if err != nil {
			slog.Warn("failed to determine cache usage record type",
				"resultID", ownerID,
				"usageIdentity", identity,
				"err", err)
		}
		if sizeBytes < 0 {
			sizeBytes = 0
		}
		measurementByIdentity[identity] = cacheUsageIdentityMeasurement{
			sizeBytes:  sizeBytes,
			recordType: recordType,
		}
	}

	published := make(map[sharedResultID]map[string]cacheUsageIdentityMeasurement, len(inputs))
	for _, input := range inputs {
		resultMeasurements := make(map[string]cacheUsageIdentityMeasurement)
		for _, identity := range input.identities {
			if ownerByIdentity[identity] != input.resultID {
				continue
			}
			measurement, ok := measurementByIdentity[identity]
			if !ok {
				continue
			}
			resultMeasurements[identity] = measurement
		}
		if len(resultMeasurements) == 0 {
			continue
		}
		published[input.resultID] = resultMeasurements
	}

	return published
}

func (c *Cache) publishUsageMeasurements(measurements map[sharedResultID]map[string]cacheUsageIdentityMeasurement) {
	c.egraphMu.Lock()
	defer c.egraphMu.Unlock()
	for resultID, res := range c.resultsByID {
		if res == nil {
			continue
		}
		resultMeasurements, ok := measurements[resultID]
		if !ok {
			res.cacheUsageSizeByIdentity = nil
			res.cacheUsageRecordTypeByID = nil
			continue
		}
		sizeByIdentity := make(map[string]int64, len(resultMeasurements))
		recordTypeByIdentity := make(map[string]string, len(resultMeasurements))
		for identity, measurement := range resultMeasurements {
			sizeByIdentity[identity] = measurement.sizeBytes
			if measurement.recordType != "" {
				recordTypeByIdentity[identity] = measurement.recordType
			}
		}
		res.cacheUsageSizeByIdentity = sizeByIdentity
		res.cacheUsageRecordTypeByID = recordTypeByIdentity

		recordTypes := cacheUsageRecordTypesFromMap(recordTypeByIdentity)
		if len(recordTypes) > 0 {
			res.recordType = cacheUsagePrimaryRecordType(recordTypes, "")
		}
	}
}

// Core cache lookup/insert flow is intentionally centralized here.
func (c *Cache) GetOrInitCall(
	ctx context.Context,
	sessionID string,
	resolver TypeResolver,
	req *CallRequest,
	fn func(context.Context) (AnyResult, error),
) (AnyResult, error) {
	if sessionID == "" {
		return nil, errors.New("get or init call: empty session ID")
	}
	return c.getOrInitCall(ctx, sessionID, resolver, req, fn)
}

//nolint:gocyclo // Core cache lookup/insert flow is intentionally centralized here.
func (c *Cache) getOrInitCall(
	ctx context.Context,
	sessionID string,
	resolver TypeResolver,
	req *CallRequest,
	fn func(context.Context) (AnyResult, error),
) (AnyResult, error) {
	if sessionID == "" {
		return nil, errors.New("get or init call: empty session ID")
	}
	if resolver == nil {
		return nil, errors.New("get or init call: type resolver is nil")
	}
	if req == nil || req.ResultCall == nil {
		return nil, fmt.Errorf("call request is nil")
	}
	ctx = ContextWithCall(ctx, req.ResultCall)

	if req.DoNotCache {
		// don't cache, don't dedupe calls, just call it

		val, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		if val == nil {
			return nil, nil
		}
		if shared := val.cacheSharedResult(); shared != nil && shared.id != 0 {
			touchSharedResultLastUsed(shared, time.Now().UnixNano())
			normalized, err := wrapSharedResultWithResolver(ctx, shared, false, resolver)
			if err != nil {
				return nil, fmt.Errorf("normalize do-not-cache attached result: %w", err)
			}
			c.trackSessionResult(ctx, sessionID, normalized, false)
			c.captureSessionResultInstallSpan(ctx, sessionID, normalized)
			return normalized, nil
		}
		if lazyEval := lazyEvalFuncOfResult(val); lazyEval != nil {
			return nil, fmt.Errorf("do-not-cache result %T cannot be lazy", val.Unwrap())
		}

		detached := &sharedResult{
			self:            val.Unwrap(),
			resultCall:      req.ResultCall.clone(),
			materialization: materializationState{realized: true},
		}
		if shared := val.cacheSharedResult(); shared != nil {
			detached.sessionResourceHandle = shared.sessionResourceHandle
			if shared.requiredSessionResources != nil {
				detached.requiredSessionResources = shared.requiredSessionResources.Copy()
			}
		}
		if onReleaser, ok := UnwrapAs[OnReleaser](val); ok {
			return nil, fmt.Errorf("do-not-cache result %T cannot implement OnReleaser", onReleaser)
		}
		detached.isObject, detached.objClass, err = resultIsObject(val, resolver)
		if err != nil {
			return nil, fmt.Errorf("classify do-not-cache result: %w", err)
		}
		if detached.isObject {
			normalized, err := wrapSharedResultWithResolver(ctx, detached, false, resolver)
			if err != nil {
				return nil, fmt.Errorf("normalize do-not-cache object result: %w", err)
			}
			return normalized, nil
		}
		return Result[Typed]{shared: detached}, nil
	}

	callDigest, err := req.deriveRecipeDigest(c)
	if err != nil {
		return nil, fmt.Errorf("derive request digest: %w", err)
	}
	requestSelf, requestInputRefs, err := req.selfDigestAndInputRefs(c)
	if err != nil {
		return nil, fmt.Errorf("derive request term digests: %w", err)
	}
	requestInputs := make([]digest.Digest, 0, len(requestInputRefs))
	for _, ref := range requestInputRefs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			return nil, fmt.Errorf("derive request term input digest: %w", err)
		}
		requestInputs = append(requestInputs, dig)
	}
	callKey := callDigest.String()
	if ctx.Value(cacheContextKey{callKey}) != nil {
		return nil, ErrCacheRecursiveCall
	}
	callConcKeys := callConcurrencyKeys{
		callKey:        callKey,
		concurrencyKey: req.ConcurrencyKey,
	}

	hitRes, hit, err := c.lookupCacheForRequest(ctx, sessionID, resolver, req, callDigest, requestSelf, requestInputs, requestInputRefs)
	if err != nil {
		return nil, err
	}
	if hit {
		outcome := cacheServeHitLive
		if shared := hitRes.cacheSharedResult(); shared != nil && shared.restored {
			outcome = cacheServeHitRestored
		}
		c.classifyServeOutcome(ctx, outcome, req.ResultCall, hitRes.cacheSharedResult().id)
		c.captureSessionLazySpanContext(ctx, sessionID, hitRes)
		c.captureSessionResultInstallSpan(ctx, sessionID, hitRes)
		return hitRes, nil
	}

	c.callsMu.Lock()
	if c.ongoingCalls == nil {
		c.ongoingCalls = make(map[callConcurrencyKeys]*ongoingCall)
	}

	if req.ConcurrencyKey != "" {
		if oc := c.ongoingCalls[callConcKeys]; oc != nil {
			if req.IsPersistable {
				oc.isPersistable = true
			}
			// already an ongoing call
			oc.waiters++
			c.callsMu.Unlock()
			return c.wait(ctx, sessionID, resolver, oc, req)
		}
	}

	// Intentional tradeoff: we do not perform a second e-graph lookup while
	// holding callsMu. A concurrent completion can index and drop its
	// singleflight entry between the first lookup and this point, which may lead
	// to occasional redundant execution instead of a late cache hit. We accept
	// that waste to avoid paying an extra lookup on this miss path.

	// make a new call with ctx that's only canceled when all caller contexts are canceled
	callCtx := context.WithValue(ctx, cacheContextKey{callKey}, struct{}{})
	callCtx, cancel := context.WithCancelCause(context.WithoutCancel(callCtx))
	sharedWorkCtx, releaseSharedWorkLease, err := withOperationLease(withoutOperationLease(callCtx))
	if err != nil {
		cancel(err)
		c.callsMu.Unlock()
		return nil, fmt.Errorf("acquire shared operation lease: %w", err)
	}
	c.classifyServeOutcome(ctx, cacheServeMissFirst, req.ResultCall, 0)
	oc := &ongoingCall{
		callConcurrencyKeys:      callConcKeys,
		isPersistable:            req.IsPersistable,
		ttlSeconds:               req.TTL,
		waitCh:                   make(chan struct{}),
		cancel:                   cancel,
		waiters:                  1,
		sharedWorkCtx:            sharedWorkCtx,
		releaseSharedWorkLeaseFn: releaseSharedWorkLease,
	}

	if req.ConcurrencyKey != "" {
		c.ongoingCalls[callConcKeys] = oc
	}

	go func() {
		defer close(oc.waitCh)
		val, err := fn(oc.sharedWorkCtx)
		oc.err = err
		oc.val = val

		c.callsMu.Lock()
		noWaiters := oc.waiters == 0
		c.callsMu.Unlock()
		if err != nil || noWaiters {
			_ = oc.releaseSharedWorkLease(context.WithoutCancel(oc.sharedWorkCtx))
		}
	}()

	c.callsMu.Unlock()
	return c.wait(ctx, sessionID, resolver, oc, req)
}

func (c *Cache) lookupCallRequest(
	ctx context.Context,
	sessionID string,
	resolver TypeResolver,
	req *CallRequest,
) (AnyResult, bool, error) {
	if sessionID == "" {
		return nil, false, errors.New("lookup call request: empty session ID")
	}
	if resolver == nil {
		return nil, false, errors.New("lookup call request: type resolver is nil")
	}
	if req == nil || req.ResultCall == nil {
		return nil, false, fmt.Errorf("call request is nil")
	}

	callDigest, err := req.deriveRecipeDigest(c)
	if err != nil {
		return nil, false, fmt.Errorf("derive request digest: %w", err)
	}
	requestSelf, requestInputRefs, err := req.selfDigestAndInputRefs(c)
	if err != nil {
		return nil, false, fmt.Errorf("derive request term digests: %w", err)
	}
	requestInputs := make([]digest.Digest, 0, len(requestInputRefs))
	for _, ref := range requestInputRefs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			return nil, false, fmt.Errorf("derive request term input digest: %w", err)
		}
		requestInputs = append(requestInputs, dig)
	}

	hitRes, hit, err := c.lookupCacheForRequest(ctx, sessionID, resolver, req, callDigest, requestSelf, requestInputs, requestInputRefs)
	if err != nil {
		return nil, false, err
	}
	if !hit {
		return nil, false, nil
	}
	return hitRes, true, nil
}

func (c *Cache) lookupCacheForDigests(
	ctx context.Context,
	sessionID string,
	resolver TypeResolver,
	recipeDigest digest.Digest,
	extraDigests []call.ExtraDigest,
) (AnyResult, bool, error) {
	if sessionID == "" {
		return nil, false, errors.New("lookup cache for digests: empty session ID")
	}
	if resolver == nil {
		return nil, false, errors.New("lookup cache for digests: type resolver is nil")
	}
	if recipeDigest == "" {
		return nil, false, nil
	}

	c.egraphMu.Lock()
	now := time.Now()
	nowUnix := now.Unix()
	match := c.lookupMatchForDigestsLocked(recipeDigest, extraDigests, nowUnix)
	c.traceLookupAttempt(ctx, recipeDigest.String(), "", nil, false)
	hitRes := c.selectLookupCandidateForSessionLocked(sessionID, match.candidates)
	if hitRes == nil {
		c.traceLookupMissNoMatch(ctx, recipeDigest.String(), false, -1, "", 0)
		c.egraphMu.Unlock()
		return nil, false, nil
	}

	hitRes.expiresAtUnix = mergeSharedResultExpiryUnix(
		hitRes.expiresAtUnix,
		candidateSharedResultExpiryUnix(nowUnix, 0),
	)
	touchSharedResultLastUsed(hitRes, now.UnixNano())
	retRes := Result[Typed]{
		shared:   hitRes,
		hitCache: true,
	}
	c.traceLookupHit(ctx, recipeDigest.String(), hitRes, match.termDigest)
	hitShared := retRes.cacheSharedResult()
	if hitShared == nil || hitShared.id == 0 {
		c.egraphMu.Unlock()
		return nil, false, fmt.Errorf("lookup cache for digests: hit missing shared result ID")
	}

	trackedCount := 0
	alreadyTracked := false
	c.sessionMu.Lock()
	if c.sessionResultIDsBySession == nil {
		c.sessionResultIDsBySession = make(map[string]map[sharedResultID]struct{})
	}
	if c.sessionResultIDsBySession[sessionID] == nil {
		c.sessionResultIDsBySession[sessionID] = make(map[sharedResultID]struct{})
	}
	if _, found := c.sessionResultIDsBySession[sessionID][hitShared.id]; found {
		alreadyTracked = true
	} else {
		c.sessionResultIDsBySession[sessionID][hitShared.id] = struct{}{}
		c.incrementIncomingOwnershipLocked(ctx, hitShared)
	}
	trackedCount = len(c.sessionResultIDsBySession[sessionID])
	c.sessionMu.Unlock()
	c.egraphMu.Unlock()

	loadedHit, err := c.ensurePersistedHitValueLoaded(ctx, resolver, retRes)
	if err != nil {
		demoted, hitErr := c.releaseFailedHit(ctx, sessionID, hitShared, alreadyTracked, err)
		return nil, false, ifNotDemoted(demoted, hitErr)
	}
	if c.traceEnabled() {
		c.traceSessionResultTracked(ctx, sessionID, loadedHit, true, trackedCount)
	}
	return loadedHit, true, nil
}

// releaseFailedHit undoes a hit's session tracking after its value failed to
// materialize. Source exhaustion is consumed here: the exhausted result
// drops — with its dependents — and the invocation proceeds as a miss,
// executing live, publishing, and re-teaching equivalence to heal the
// store. Transient source unavailability demotes the same way — the run
// computes honestly — but drops nothing: the row's sources were not proven
// dead, so the next lookup's walk retries them. Any other failure
// propagates.
func (c *Cache) releaseFailedHit(ctx context.Context, sessionID string, hitShared *sharedResult, alreadyTracked bool, err error) (demoted bool, rerr error) {
	c.egraphMu.Lock()
	c.sessionMu.Lock()
	if resultIDs := c.sessionResultIDsBySession[sessionID]; resultIDs != nil {
		delete(resultIDs, hitShared.id)
		if len(resultIDs) == 0 {
			delete(c.sessionResultIDsBySession, sessionID)
		}
	}
	c.sessionMu.Unlock()
	queue := []*sharedResult(nil)
	var decErr error
	if !alreadyTracked {
		queue, decErr = c.decrementIncomingOwnershipLocked(ctx, hitShared, nil)
	}
	collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
	c.egraphMu.Unlock()
	releaseErr := runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)
	if errors.Is(err, errSourcesExhausted) {
		c.classifyServeOutcome(ctx, cacheServeDemotedToMiss, hitShared.loadResultCall(), hitShared.id)
		c.traceHitDemotedToMiss(ctx, hitShared, err)
		return true, errors.Join(decErr, collectErr, releaseErr, c.dropExhaustedResult(ctx, hitShared))
	}
	if errors.Is(err, errSourcesUnavailable) {
		c.classifyServeOutcome(ctx, cacheServeDemotedToMiss, hitShared.loadResultCall(), hitShared.id)
		c.traceHitDemotedToMiss(ctx, hitShared, err)
		return true, errors.Join(decErr, collectErr, releaseErr)
	}
	return false, errors.Join(err, decErr, collectErr, releaseErr)
}

// ifNotDemoted keeps a consumed demote silent: a demoted hit with clean
// bookkeeping returns no error so the invocation proceeds as a miss.
func ifNotDemoted(demoted bool, err error) error {
	if demoted && err == nil {
		return nil
	}
	return err
}

func (c *Cache) wait(
	ctx context.Context,
	sessionID string,
	resolver TypeResolver,
	oc *ongoingCall,
	req *CallRequest,
) (AnyResult, error) {
	var (
		completionErr error
		canceledErr   error
		completed     bool
	)

	select {
	case <-oc.waitCh:
		completed = true
	case <-ctx.Done():
		canceledErr = context.Cause(ctx)
	}

	if completed {
		completionErr = oc.err
	}

	if !completed {
		c.callsMu.Lock()
		oc.waiters--
		lastWaiter := oc.waiters == 0
		releaseHandoff := lastWaiter && oc.handoffHoldActive
		if lastWaiter {
			delete(c.ongoingCalls, oc.callConcurrencyKeys)
			oc.cancel(canceledErr)
		}
		c.callsMu.Unlock()
		if releaseHandoff && oc.res != nil {
			c.egraphMu.Lock()
			queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
			collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
			c.egraphMu.Unlock()
			oc.handoffHoldActive = false
			if relErr := errors.Join(decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)); relErr != nil {
				return nil, errors.Join(canceledErr, relErr)
			}
		}
		return nil, canceledErr
	}

	if completionErr != nil {
		c.callsMu.Lock()
		oc.waiters--
		lastWaiter := oc.waiters == 0
		if lastWaiter {
			delete(c.ongoingCalls, oc.callConcurrencyKeys)
			oc.cancel(completionErr)
		}
		c.callsMu.Unlock()
		return nil, completionErr
	}

	oc.initCompletedResultOnce.Do(func() {
		defer func() {
			_ = oc.releaseSharedWorkLease(context.WithoutCancel(oc.sharedWorkCtx))
		}()
		oc.initCompletedResultErr = c.initCompletedResult(context.WithoutCancel(oc.sharedWorkCtx), resolver, oc, req, sessionID)
		c.callsMu.Lock()
		delete(c.ongoingCalls, oc.callConcurrencyKeys)
		c.callsMu.Unlock()
	})
	// TODO there's a race condition here: thread one enters the .Do() above but hasn't finished calling initCompletedResult(....), the second thread will skip over the Do(),
	// then check the err below before it's actually written to
	if oc.initCompletedResultErr != nil {
		c.callsMu.Lock()
		oc.waiters--
		lastWaiter := oc.waiters == 0
		c.callsMu.Unlock()
		if lastWaiter && oc.handoffHoldActive {
			c.egraphMu.Lock()
			queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
			collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
			c.egraphMu.Unlock()
			oc.handoffHoldActive = false
			if relErr := errors.Join(decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)); relErr != nil {
				return nil, relErr
			}
		}
		return nil, oc.initCompletedResultErr
	}
	if oc.res == nil {
		c.callsMu.Lock()
		oc.waiters--
		lastWaiter := oc.waiters == 0
		c.callsMu.Unlock()
		if lastWaiter && oc.handoffHoldActive {
			c.egraphMu.Lock()
			queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
			collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
			c.egraphMu.Unlock()
			oc.handoffHoldActive = false
			if relErr := errors.Join(decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)); relErr != nil {
				return nil, relErr
			}
		}
		return nil, fmt.Errorf("cache wait completed without initialized result")
	}

	touchSharedResultLastUsed(oc.res, time.Now().UnixNano())

	retRes := Result[Typed]{
		shared:   oc.res,
		hitCache: false,
	}
	c.trackSessionResult(ctx, sessionID, retRes, false)
	c.captureSessionLazySpanContext(ctx, sessionID, retRes)
	c.captureSessionResultInstallSpan(ctx, sessionID, retRes)
	c.callsMu.Lock()
	oc.waiters--
	lastWaiter := oc.waiters == 0
	c.callsMu.Unlock()
	if lastWaiter && oc.handoffHoldActive {
		c.egraphMu.Lock()
		queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
		collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
		c.egraphMu.Unlock()
		oc.handoffHoldActive = false
		if relErr := errors.Join(decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases)); relErr != nil {
			return nil, relErr
		}
	}

	retResAny, err := c.ensurePersistedHitValueLoaded(ctx, resolver, retRes)
	if err != nil {
		// Reachable for restored rows when the call's function returned an
		// existing cache-backed result and the canonical pick adopted an
		// exhausted sibling: there is no fresh value left to fall back to
		// (adoption discarded it), so this use fails honestly and the drop
		// heals the next lookup.
		return nil, fmt.Errorf("wait: normalize returned result: %w", c.normalizeExhaustedResultError(ctx, oc.res, err))
	}
	return retResAny, nil
}

//nolint:gocyclo // intrinsically long state machine; refactoring would hurt clarity
func (c *Cache) initCompletedResult(ctx context.Context, resolver TypeResolver, oc *ongoingCall, req *CallRequest, sessionID string) error {
	resWasCacheBacked := false
	now := time.Now()
	var (
		resultTermSelf   digest.Digest
		resultTermInputs []digest.Digest
		resultTermRefs   []ResultCallStructuralInputRef
		hasResultTerm    bool
	)
	if req == nil || req.ResultCall == nil {
		return fmt.Errorf("call request is nil")
	}
	finishAttachDeps := func(err error) {
		if resWasCacheBacked || oc.res == nil {
			return
		}
		oc.res.attachDepsMu.Lock()
		if oc.res.attachDepsWaitCh != nil {
			oc.res.attachDepsErr = err
			close(oc.res.attachDepsWaitCh)
		}
		oc.res.attachDepsMu.Unlock()
	}

	// Materialize shared result for this completed call.
	oc.res = &sharedResult{}
	if oc.val != nil {
		if existingRes := oc.val.cacheSharedResult(); existingRes != nil && existingRes.id != 0 {
			c.egraphMu.Lock()
			oc.res = c.canonicalEquivalentSharedResultLocked(sessionID, existingRes, time.Now().Unix())
			// Take the publication handoff hold inside the same critical
			// section as the canonical pick: the adopted result may be owned
			// only by another session, and a concurrent session release must
			// not be able to collect it (running its OnRelease) before this
			// call re-acquires the lock and its waiters claim session
			// ownership.
			c.incrementIncomingOwnershipLocked(ctx, oc.res)
			oc.handoffHoldActive = true
			c.egraphMu.Unlock()
			if objVal, ok := oc.val.(AnyObjectResult); ok {
				oc.res.setObjClass(objVal.ObjectType())
			}

			resWasCacheBacked = true
		} else {
			oc.res.self = oc.val.Unwrap()
			if shared := oc.val.cacheSharedResult(); shared != nil {
				if frame := shared.loadResultCall(); frame != nil {
					oc.res.storeResultCall(frame.clone())
					c.traceResultCallFrameUpdated(ctx, oc.res, "init_completed_result_existing_value_frame", nil, oc.res.loadResultCall())
				}
				oc.res.sessionResourceHandle = shared.sessionResourceHandle
				if shared.requiredSessionResources != nil {
					oc.res.requiredSessionResources = shared.requiredSessionResources.Copy()
				}
			}
			if oc.res.loadResultCall() == nil {
				oc.res.storeResultCall(req.ResultCall.clone())
				c.traceResultCallFrameUpdated(ctx, oc.res, "init_completed_result_request_frame", nil, oc.res.loadResultCall())
			}
			oc.res.materialization.realized = true

			if onReleaser, ok := UnwrapAs[OnReleaser](oc.val); ok {
				oc.res.onRelease = onReleaser.OnRelease
			}
			isObject, objClass, err := resultIsObject(oc.val, resolver)
			if err != nil {
				return fmt.Errorf("classify completed result: %w", err)
			}
			oc.res.isObject = isObject
			oc.res.objClass = objClass
		}
	}
	if !resWasCacheBacked {
		oc.res.onRelease = joinOnRelease(c.resultSnapshotLeaseCleanup(oc.res), oc.res.onRelease)
	}
	requestForIndex := req
	if oc.res.createdAtUnixNano == 0 {
		oc.res.createdAtUnixNano = now.UnixNano()
	}
	touchSharedResultLastUsed(oc.res, now.UnixNano())
	if oc.res.recordType == "" {
		oc.res.recordType = requestForIndex.Field
	}
	if oc.res.recordType == "" {
		oc.res.recordType = "dagql.unknown"
	}
	if oc.res.description == "" {
		oc.res.description = requestForIndex.Field
	}
	if oc.res.description == "" {
		if reqDig, err := requestForIndex.deriveRecipeDigest(c); err == nil {
			oc.res.description = reqDig.String()
		}
	}
	// TTL merge policy for shared results:
	// - 0 means "no TTL for this writer", not necessarily "never expire globally".
	// - if any writer provides TTL, we keep the earliest non-zero expiry.
	// - 0 only remains when all writers are 0.
	oc.res.expiresAtUnix = mergeSharedResultExpiryUnix(
		oc.res.expiresAtUnix,
		candidateSharedResultExpiryUnix(now.Unix(), oc.ttlSeconds),
	)
	if !resWasCacheBacked {
		if resultCall := oc.res.loadResultCall(); resultCall != nil {
			selfDigest, inputRefs, deriveErr := resultCall.selfDigestAndInputRefs(c)
			if deriveErr != nil {
				return fmt.Errorf("derive result term digests: %w", deriveErr)
			}
			inputDigests := make([]digest.Digest, 0, len(inputRefs))
			for _, ref := range inputRefs {
				dig, err := ref.inputDigest(c)
				if err != nil {
					return fmt.Errorf("derive result term input digest: %w", err)
				}
				inputDigests = append(inputDigests, dig)
			}
			resultTermSelf = selfDigest
			resultTermInputs = inputDigests
			resultTermRefs = inputRefs
			hasResultTerm = true
		}
	}

	requestDigest, err := requestForIndex.deriveRecipeDigest(c)
	if err != nil {
		return fmt.Errorf("derive request digest: %w", err)
	}
	requestSelf, requestInputRefs, err := requestForIndex.selfDigestAndInputRefs(c)
	if err != nil {
		return fmt.Errorf("derive request term digests: %w", err)
	}
	requestInputs := make([]digest.Digest, 0, len(requestInputRefs))
	for _, ref := range requestInputRefs {
		dig, err := ref.inputDigest(c)
		if err != nil {
			return fmt.Errorf("derive request term input digest: %w", err)
		}
		requestInputs = append(requestInputs, dig)
	}
	var responseDigest digest.Digest
	if resultCall := oc.res.loadResultCall(); resultCall != nil {
		responseDigest, err = resultCall.deriveRecipeDigest(c)
		if err != nil {
			return fmt.Errorf("derive result digest: %w", err)
		}
	}
	type resultCallDep struct {
		resultID sharedResultID
		path     string
	}
	var resultCallDeps []resultCallDep
	if !resWasCacheBacked {
		if resultCall := oc.res.loadResultCall(); resultCall != nil {
			seenResults := map[sharedResultID]struct{}{}
			seenCalls := map[*ResultCall]struct{}{}

			var joinPath func(string, string) string
			var walkFrame func(string, *ResultCall) error
			var walkRef func(string, *ResultCallRef) error
			var walkLiteral func(string, *ResultCallLiteral) error

			joinPath = func(prefix string, segment string) string {
				switch {
				case prefix == "":
					return segment
				case segment == "":
					return prefix
				default:
					return prefix + "." + segment
				}
			}

			walkRef = func(path string, ref *ResultCallRef) error {
				if ref == nil {
					return nil
				}
				if ref.Call != nil {
					return walkFrame(path, ref.Call)
				}
				if ref.ResultID == 0 {
					return nil
				}
				resultID := sharedResultID(ref.ResultID)
				if resultID == oc.res.id {
					return nil
				}
				if _, seen := seenResults[resultID]; seen {
					return nil
				}
				seenResults[resultID] = struct{}{}
				resultCallDeps = append(resultCallDeps, resultCallDep{
					resultID: resultID,
					path:     path,
				})
				return nil
			}

			walkLiteral = func(path string, lit *ResultCallLiteral) error {
				if lit == nil {
					return nil
				}
				switch lit.Kind {
				case ResultCallLiteralKindResultRef:
					return walkRef(path, lit.ResultRef)
				case ResultCallLiteralKindList:
					for i, item := range lit.ListItems {
						if err := walkLiteral(fmt.Sprintf("%s[%d]", path, i), item); err != nil {
							return err
						}
					}
				case ResultCallLiteralKindObject:
					for _, field := range lit.ObjectFields {
						if field == nil {
							continue
						}
						if err := walkLiteral(joinPath(path, field.Name), field.Value); err != nil {
							return err
						}
					}
				}
				return nil
			}

			walkFrame = func(path string, frame *ResultCall) error {
				if frame == nil {
					return nil
				}
				if _, seen := seenCalls[frame]; seen {
					return nil
				}
				seenCalls[frame] = struct{}{}

				if err := walkRef(joinPath(path, "receiver"), frame.Receiver); err != nil {
					return fmt.Errorf("receiver: %w", err)
				}
				if frame.Module != nil {
					if err := walkRef(joinPath(path, "module"), frame.Module.ResultRef); err != nil {
						return fmt.Errorf("module: %w", err)
					}
				}
				for _, arg := range frame.Args {
					if arg == nil {
						continue
					}
					if err := walkLiteral(joinPath(path, "arg:"+arg.Name), arg.Value); err != nil {
						return fmt.Errorf("arg %q: %w", arg.Name, err)
					}
				}
				for _, input := range frame.ImplicitInputs {
					if input == nil {
						continue
					}
					if err := walkLiteral(joinPath(path, "implicit_input:"+input.Name), input.Value); err != nil {
						return fmt.Errorf("implicit input %q: %w", input.Name, err)
					}
				}
				return nil
			}

			if err := walkFrame("", resultCall); err != nil {
				return fmt.Errorf("collect result call dependencies: %w", err)
			}
		}
	}

	c.egraphMu.Lock()
	resultCall := oc.res.loadResultCall()
	indexErr := c.indexWaitResultInEgraphLocked(
		ctx,
		requestForIndex.ResultCall,
		resultCall,
		requestDigest,
		responseDigest,
		requestSelf,
		requestInputs,
		requestInputRefs,
		resultTermSelf,
		resultTermInputs,
		resultTermRefs,
		hasResultTerm,
		oc.res,
	)
	if indexErr != nil {
		c.egraphMu.Unlock()
		return indexErr
	}
	for _, dep := range resultCallDeps {
		depID := dep.resultID
		depRes := c.resultsByID[depID]
		if depRes == nil {
			c.egraphMu.Unlock()
			return fmt.Errorf("retain result call ref %d: missing cached result", depID)
		}
		if oc.res.deps == nil {
			oc.res.deps = make(map[sharedResultID]struct{})
		}
		if _, alreadyHeld := oc.res.deps[depID]; alreadyHeld {
			continue
		}
		oc.res.deps[depID] = struct{}{}
		c.rememberDependencyEdgeLocked(oc.res, depRes)
		c.incrementIncomingOwnershipLocked(ctx, depRes)
		c.traceResultCallDepAdded(ctx, oc.res.id, depID, dep.path)
	}
	if err := c.recomputeRequiredSessionResourcesLocked(oc.res); err != nil {
		c.egraphMu.Unlock()
		return err
	}
	if oc.isPersistable {
		c.upsertPersistedEdgeLocked(ctx, oc.res, candidateSharedResultExpiryUnix(now.Unix(), oc.ttlSeconds), false)
	}
	// The cache-backed path already took the handoff hold when it adopted the
	// canonical result above; only fresh results take it here.
	if !oc.handoffHoldActive {
		c.incrementIncomingOwnershipLocked(ctx, oc.res)
		oc.handoffHoldActive = true
	}
	if !resWasCacheBacked {
		oc.res.attachDepsMu.Lock()
		oc.res.attachDepsWaitCh = make(chan struct{})
		oc.res.attachDepsErr = nil
		oc.res.attachDepsMu.Unlock()
	}
	c.egraphMu.Unlock()

	if err := c.attachDependencyResults(ctx, sessionID, resolver, oc.res, oc.val); err != nil {
		c.egraphMu.Lock()
		queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
		collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
		c.egraphMu.Unlock()
		oc.handoffHoldActive = false
		attachErr := errors.Join(err, decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases))
		finishAttachDeps(attachErr)
		return attachErr
	}
	if err := c.syncResultSnapshotLeases(ctx, oc.res); err != nil {
		c.egraphMu.Lock()
		queue, decErr := c.decrementIncomingOwnershipLocked(ctx, oc.res, nil)
		collectReleases, collectErr := c.collectUnownedResultsLocked(context.WithoutCancel(ctx), queue)
		c.egraphMu.Unlock()
		oc.handoffHoldActive = false
		attachErr := errors.Join(err, decErr, collectErr, runOnReleaseFuncs(context.WithoutCancel(ctx), collectReleases))
		finishAttachDeps(attachErr)
		return attachErr
	}
	// Capture the value's lazy fragment now: publication is the one moment a
	// fresh result is guaranteed to still carry its recipe (realization
	// destroys it), and its input results are attached so their IDs resolve.
	// A capture failure costs the result its re-make fragment, nothing more;
	// the call itself already succeeded.
	if !resWasCacheBacked {
		c.freshResultCount.Add(1)
	}
	if !resWasCacheBacked && oc.val != nil {
		if fragEncoder, ok := UnwrapAs[PersistedLazyFragmentEncoder](oc.val); ok {
			frag, err := fragEncoder.EncodePersistedLazyFragment(ctx, c)
			switch {
			case err != nil:
				slog.Warn("failed to capture lazy fragment at publication",
					"sharedResultID", oc.res.id, "type", fmt.Sprintf("%T", oc.val.Unwrap()), "err", err)
			case frag != nil:
				oc.res.storeLazyFragment(frag)
			}
		}
	}
	c.registerLazyEvaluation(oc.res, oc.val)
	finishAttachDeps(nil)

	return nil
}

func (c *Cache) attachDependencyResults(ctx context.Context, sessionID string, resolver TypeResolver, parent *sharedResult, val AnyResult) error {
	if parent == nil || val == nil {
		return nil
	}
	withKinds, hasKinds := UnwrapAs[HasDependencyResultsKinds](val)
	withDeps, hasDeps := UnwrapAs[HasDependencyResults](val)
	if !hasKinds && !hasDeps {
		return nil
	}
	self := Result[Typed]{shared: parent}
	var attachedSelf AnyResult = self
	parentState := parent.loadPayloadState()
	if parentState.realized && parentState.isObject {
		objSelf, err := wrapSharedResultWithResolver(ctx, parent, false, resolver)
		if err != nil {
			return fmt.Errorf("attach dependency results: reconstruct attached self: %w", err)
		}
		attachedSelf = objSelf
	}
	attach := func(child AnyResult) (AnyResult, error) {
		return c.attachResult(ctx, sessionID, resolver, child)
	}
	var deps []DependencyResult
	if hasKinds {
		var err error
		deps, err = withKinds.AttachDependencyResultsKinds(ctx, attachedSelf, attach)
		if err != nil {
			return err
		}
	} else {
		attached, err := withDeps.AttachDependencyResults(ctx, attachedSelf, attach)
		if err != nil {
			return err
		}
		// Default: a value implementing only HasDependencyResults treats every
		// returned dep as owned (failure attribution propagates).
		deps = make([]DependencyResult, 0, len(attached))
		for _, a := range attached {
			deps = append(deps, DependencyResult{Result: a, Owned: true})
		}
	}
	if len(deps) == 0 || parent.id == 0 {
		return nil
	}

	seen := make(map[sharedResultID]bool, len(deps))
	for _, dep := range deps {
		if dep.Result == nil {
			continue
		}
		attachedDepRes := dep.Result.cacheSharedResult()
		if attachedDepRes == nil || attachedDepRes.id == 0 {
			return fmt.Errorf("attach dependency result %T: unexpected detached result", dep.Result)
		}
		if attachedDepRes.id == parent.id {
			continue
		}
		if alreadyOwned, ok := seen[attachedDepRes.id]; ok {
			// Already linked. If the previous edge was non-owned and this one is
			// owned, propagate the install span now; otherwise nothing to do.
			if !alreadyOwned && dep.Owned {
				c.captureSessionResultInstallSpan(ctx, sessionID, dep.Result)
				seen[attachedDepRes.id] = true
			}
			continue
		}
		seen[attachedDepRes.id] = dep.Owned
		if err := c.AddExplicitDependency(ctx, attachedSelf, dep.Result, "attached_dependency_result"); err != nil {
			return err
		}
		// Owned deps inherit the parent's install span — failures in their
		// lazy work will mark the parent's API span caused-failed via the
		// resume span's cause links. Liveness-only deps (e.g. lazy parents
		// in receiver chains) keep their original install spans only.
		if dep.Owned {
			c.captureSessionResultInstallSpan(ctx, sessionID, dep.Result)
		}
	}

	return nil
}

func candidateSharedResultExpiryUnix(nowUnix, ttlSeconds int64) int64 {
	if ttlSeconds <= 0 {
		return 0
	}
	return nowUnix + ttlSeconds
}

func mergeSharedResultExpiryUnix(existingExpiresAtUnix, candidateExpiresAtUnix int64) int64 {
	switch {
	case existingExpiresAtUnix == 0 && candidateExpiresAtUnix == 0:
		return 0
	case existingExpiresAtUnix == 0:
		return candidateExpiresAtUnix
	case candidateExpiresAtUnix == 0:
		return existingExpiresAtUnix
	case candidateExpiresAtUnix < existingExpiresAtUnix:
		return candidateExpiresAtUnix
	default:
		return existingExpiresAtUnix
	}
}

func cacheUsageSizeBytesFromSelf(ctx context.Context, sizeProvider CacheUsageSizeProvider, self Typed, identity string) (int64, bool, error) {
	if self == nil {
		return 0, false, nil
	}
	sizer, ok := any(self).(cacheUsageSizer)
	if !ok {
		return 0, false, nil
	}
	return sizer.CacheUsageSize(ctx, sizeProvider, identity)
}

func cacheUsageSizeBytesFromSnapshotLink(ctx context.Context, sizeProvider CacheUsageSizeProvider, identity string) (int64, bool, error) {
	if sizeProvider == nil || identity == "" {
		return 0, false, nil
	}
	sizeBytes, err := sizeProvider.SnapshotSize(ctx, identity)
	if err != nil {
		return 0, false, err
	}
	return sizeBytes, true, nil
}

func cacheUsageRecordTypeFromSnapshotMetadata(ctx context.Context, snapshotManager bkcache.SnapshotManager, identity string) (string, bool, error) {
	if snapshotManager == nil || identity == "" {
		return "", false, nil
	}
	md, ok, err := snapshotManager.SnapshotRecordMetadata(ctx, identity)
	if err != nil || !ok || md.RecordType == "" {
		return "", ok, err
	}
	return string(md.RecordType), true, nil
}

func cacheUsageIdentitiesFromSelf(self Typed) []string {
	if self == nil {
		return nil
	}
	identityer, ok := any(self).(hasCacheUsageIdentity)
	if !ok {
		return nil
	}
	ids := append([]string(nil), identityer.CacheUsageIdentities()...)
	slices.Sort(ids)
	return slices.Compact(ids)
}

func cacheUsageIdentitiesFromSnapshotLinks(links []PersistedSnapshotRefLink) []string {
	if len(links) == 0 {
		return nil
	}
	ids := make([]string, 0, len(links))
	for _, link := range links {
		if link.RefKey == "" {
			continue
		}
		ids = append(ids, link.RefKey)
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}

func cacheUsageIdentities(res *sharedResult) []string {
	if res == nil {
		return nil
	}
	state := res.loadPayloadState()
	if state.realized && state.self != nil {
		return cacheUsageIdentitiesFromSelf(state.self)
	}
	return cacheUsageIdentitiesFromSnapshotLinks(state.snapshotOwnerLinks)
}

func cacheUsageSizeMayChangeFromSelf(self Typed) bool {
	if self == nil {
		return false
	}
	mutableSizer, ok := any(self).(cacheUsageMayChange)
	if !ok {
		return false
	}
	return mutableSizer.CacheUsageMayChange()
}
