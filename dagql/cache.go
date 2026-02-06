package dagql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/dagger/dagger/dagql/call"
	cachedb "github.com/dagger/dagger/dagql/db"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/engine/slog"
	"github.com/dagger/dagger/util/hashutil"
	"github.com/opencontainers/go-digest"
	"github.com/vektah/gqlparser/v2/ast"
)

type Cache interface {
	// Using the given key, either return an already cached value for that key or initialize a
	// new value using the given function. If the function returns an error, the error is returned.
	GetOrInitialize(
		context.Context,
		CacheKey,
		func(context.Context) (AnyResult, error),
	) (AnyResult, error)

	// Returns the number of entries in the cache.
	Size() int

	// Run a blocking loop that periodically garbage collects expired entries from the cache db.
	GCLoop(context.Context)
}

func ValueFunc(v AnyResult) func(context.Context) (AnyResult, error) {
	return func(context.Context) (AnyResult, error) {
		return v, nil
	}
}

type CacheKey struct {
	ID *call.ID

	// ConcurrencyKey is used to determine whether *in-progress* calls should be deduplicated.
	// If a call with a given (ResultKey, ConcurrencyKey) pair is already in progress, and
	// another one comes in with the same pair, the second caller will wait for the first
	// to complete and receive the same result.
	//
	// If two calls have the same ResultKey but different ConcurrencyKeys, they will not be deduped.
	//
	// If ConcurrencyKey is the zero value for K, no deduplication of in-progress calls will be done.
	ConcurrencyKey string

	// TTL is the time-to-live for the cached result of this call, in seconds.
	TTL int64

	// DoNotCache indicates that this call should not be cached at all, simply ran.
	DoNotCache bool
}

type ctxStorageKey struct{}

// Get the key that should be used (or mixed into) persistent cache storage
// We smuggle this around in the context for now since we have to incorporate
// it with buildkit's persistent cache for now.
func CurrentStorageKey(ctx context.Context) string {
	if v := ctx.Value(ctxStorageKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func ctxWithStorageKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, ctxStorageKey{}, key)
}

var ErrCacheRecursiveCall = fmt.Errorf("recursive call detected")

func NewCache(ctx context.Context, dbPath string) (Cache, error) {
	c := &cache{}

	if dbPath == "" {
		return c, nil
	}

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
		return nil, fmt.Errorf("open %s: %w", connURL, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", connURL, err)
	}
	if _, err := db.Exec(cachedb.Schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	c.db, err = cachedb.Prepare(ctx, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare queries: %w", err)
	}

	return c, nil
}

type cache struct {
	mu sync.Mutex

	// calls that are in progress, keyed by a combination of the call key and the concurrency key
	// two calls with the same call+concurrency key will be "single-flighted" (only one will actually run)
	ongoingCalls map[callConcurrencyKeys]*result

	// calls that have completed successfully and are cached, keyed by the storage key
	completedCalls map[string]*resultList[string, AnyResult]

	// calls that have completed successfully and are cached, keyed by content digest key
	completedCallsByContent map[string]*resultList[string, AnyResult]

	// db for persistence; currently only used for metadata supporting ttl-based expiration
	db *cachedb.Queries
}

type callConcurrencyKeys struct {
	callKey        string
	concurrencyKey string
}

var _ Cache = &cache{}

type PostCallFunc = func(context.Context) error

type OnReleaseFunc = func(context.Context) error

type result struct {
	cache *cache

	storageKey          string              // key to cache.completedCalls
	callConcurrencyKeys callConcurrencyKeys // key to cache.ongoingCalls

	val                AnyResult
	err                error
	safeToPersistCache bool

	// optional content digest key set on the result
	contentDigestKey string
	// optional result call key set on the result (see ValueWithCallbacks.ResultCallKey)
	resultCallKey string

	persistToDB func(context.Context) error
	postCall    PostCallFunc
	onRelease   OnReleaseFunc

	waitCh  chan struct{}
	cancel  context.CancelCauseFunc
	waiters int

	refCount int
}

type Result[T Typed] struct {
	constructor        *call.ID
	self               T
	postCall           PostCallFunc
	safeToPersistCache bool

	hitCache              bool
	hitContentDigestCache bool
}

var _ AnyResult = Result[Typed]{}

func (o Result[T]) Type() *ast.Type {
	return o.self.Type()
}

// ID returns the ID of the instance.
func (r Result[T]) ID() *call.ID {
	return r.constructor
}

func (r Result[T]) Self() T {
	return r.self
}

func (r Result[T]) SetField(field reflect.Value) error {
	return assign(field, r.self)
}

// Unwrap returns the inner value of the instance.
func (r Result[T]) Unwrap() Typed {
	return r.self
}

func (r Result[T]) DerefValue() (AnyResult, bool) {
	derefableSelf, ok := any(r.self).(DerefableResult)
	if !ok {
		return r, true
	}
	return derefableSelf.DerefToResult(r.constructor, r.postCall)
}

func (r Result[T]) NthValue(nth int) (AnyResult, error) {
	enumerableSelf, ok := any(r.self).(Enumerable)
	if !ok {
		return nil, fmt.Errorf("cannot get %dth value from %T", nth, r.self)
	}
	return enumerableSelf.NthValue(nth, r.constructor)
}

func (r Result[T]) WithPostCall(fn PostCallFunc) AnyResult {
	r.postCall = fn
	return r
}

func (r Result[T]) ResultWithPostCall(fn PostCallFunc) Result[T] {
	r.postCall = fn
	return r
}

func (r Result[T]) WithSafeToPersistCache(safe bool) AnyResult {
	r.safeToPersistCache = safe
	return r
}

func (r Result[T]) IsSafeToPersistCache() bool {
	return r.safeToPersistCache
}

// WithDigest returns an updated instance with the given metadata set.
// customDigest overrides the default digest of the instance to the provided value.
// NOTE: customDigest must be used with care as any instances with the same digest
// will be considered equivalent and can thus replace each other in the cache.
// Generally, customDigest should be used when there's a content-based digest available
// that won't be caputured by the default, call-chain derived digest.
func (r Result[T]) WithDigest(customDigest digest.Digest) Result[T] {
	return Result[T]{
		constructor:        r.constructor.WithDigest(customDigest),
		self:               r.self,
		postCall:           r.postCall,
		safeToPersistCache: r.safeToPersistCache,
	}
}

func (r Result[T]) WithContentDigest(customDigest digest.Digest) Result[T] {
	return Result[T]{
		constructor:        r.constructor.With(call.WithContentDigest(customDigest)),
		self:               r.self,
		postCall:           r.postCall,
		safeToPersistCache: r.safeToPersistCache,
	}
}

// String returns the instance in Class@sha256:... format.
func (r Result[T]) String() string {
	return fmt.Sprintf("%s@%s", r.self.Type().Name(), r.constructor.Digest())
}

func (r Result[T]) PostCall(ctx context.Context) error {
	if r.postCall != nil {
		return r.postCall(ctx)
	}
	return nil
}

func (r Result[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.ID())
}

func (r Result[T]) WithID(id *call.ID) AnyResult {
	return Result[T]{
		constructor:        id,
		self:               r.self,
		postCall:           r.postCall,
		safeToPersistCache: r.safeToPersistCache,
	}
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
	derefableSelf, ok := any(r.self).(DerefableResult)
	if !ok {
		return r, true
	}
	return derefableSelf.DerefToResult(r.constructor, r.postCall)
}

func (r ObjectResult[T]) SetField(field reflect.Value) error {
	return assign(field, r.Result)
}

// ObjectType returns the ObjectType of the instance.
func (r ObjectResult[T]) ObjectType() ObjectType {
	return r.class
}

func (r ObjectResult[T]) WithObjectDigest(customDigest digest.Digest) ObjectResult[T] {
	return ObjectResult[T]{
		Result: Result[T]{
			constructor:        r.constructor.WithDigest(customDigest),
			self:               r.self,
			postCall:           r.postCall,
			safeToPersistCache: r.safeToPersistCache,
		},
		class: r.class,
	}
}

func (r ObjectResult[T]) WithContentDigest(customDigest digest.Digest) ObjectResult[T] {
	return ObjectResult[T]{
		Result: Result[T]{
			constructor:        r.constructor.With(call.WithContentDigest(customDigest)),
			self:               r.self,
			postCall:           r.postCall,
			safeToPersistCache: r.safeToPersistCache,
		},
		class: r.class,
	}
}

func (r ObjectResult[T]) WithID(id *call.ID) AnyResult {
	return ObjectResult[T]{
		Result: Result[T]{
			constructor:        id,
			self:               r.self,
			postCall:           r.postCall,
			safeToPersistCache: r.safeToPersistCache,
		},
		class: r.class,
	}
}

func (r ObjectResult[T]) ObjectResultWithPostCall(fn PostCallFunc) ObjectResult[T] {
	r.postCall = fn
	return r
}

type cacheContextKey struct {
	key   string
	cache *cache
}

func (c *cache) GCLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now().Unix()
		if err := c.db.GCExpiredCalls(ctx, cachedb.GCExpiredCallsParams{
			Now: now,
		}); err != nil {
			slog.Warn("failed to GC expired function calls", "err", err)
		}
	}
}

func (c *cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	total := len(c.ongoingCalls)
	for _, lst := range c.completedCalls {
		total += lst.len()
	}
	for _, lst := range c.completedCallsByContent {
		total += lst.len()
	}
	return total
}

func (c *cache) GetOrInitialize(
	ctx context.Context,
	key CacheKey,
	fn func(context.Context) (AnyResult, error),
) (AnyResult, error) {
	if key.DoNotCache {
		// don't cache, don't dedupe calls, just call it

		// we currently still have to appease the buildkit cache key machinery underlying function calls,
		// so make sure it gets a random storage key
		ctx = ctxWithStorageKey(ctx, rand.Text())

		valWithCallbacks, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		res := &perCallResult[K, V]{result: &result[K, V]{}}
		if valWithCallbacks != nil {
			res.val = valWithCallbacks.Value
			res.postCall = valWithCallbacks.PostCall
			res.onRelease = valWithCallbacks.OnRelease
		}
		return res, nil
	}

	callKey := string(key.CallKey)
	callConcKeys := callConcurrencyKeys{
		callKey:        callKey,
		concurrencyKey: string(key.ConcurrencyKey),
	}

	// The storage key is the key for what's actually stored on disk.
	// By default it's just the call key, but if we have a TTL then there
	// can be different results stored on disk for a single call key, necessitating
	// this separate storage key.
	storageKey := callKey

	var persistToDB func(context.Context) error
	if key.TTL != 0 && c.db != nil {
		call, err := c.db.SelectCall(ctx, callKey)
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			now := time.Now().Unix()
			expiration := now + key.TTL

			// TODO:(sipsma) we unfortunately have to incorporate the session ID into the storage key
			// for now in order to get functions that make SetSecret calls to behave as "per-session"
			// caches (while *also* retaining the correct behavior in all other cases). It would be
			// nice to find some more elegant way of modeling this that disentangles this cache
			// from engine client metadata.
			switch {
			case call == nil:
				md, err := engine.ClientMetadataFromContext(ctx)
				if err != nil {
					return nil, fmt.Errorf("get client metadata: %w", err)
				}
				storageKey = hashutil.NewHasher().
					WithString(storageKey).
					WithString(md.SessionID).
					DigestAndClose()

				// Nothing saved in the cache yet, use a new expiration. Don't save yet, that only happens
				// once a call completes successfully and has been determined to be safe to cache.
				persistToDB = func(ctx context.Context) error {
					return c.db.SetExpiration(ctx, cachedb.SetExpirationParams{
						CallKey:        string(key.CallKey),
						StorageKey:     storageKey,
						Expiration:     expiration,
						PrevStorageKey: "",
					})
				}

			case call.Expiration < now:
				md, err := engine.ClientMetadataFromContext(ctx)
				if err != nil {
					return nil, fmt.Errorf("get client metadata: %w", err)
				}
				storageKey = hashutil.NewHasher().
					WithString(storageKey).
					WithString(md.SessionID).
					DigestAndClose()

				// We do have a cached entry, but it expired, so don't use it. Use a new expiration, but again
				// don't store it yet until the call completes successfully and is determined to be safe
				// to cache.
				persistToDB = func(ctx context.Context) error {
					return c.db.SetExpiration(ctx, cachedb.SetExpirationParams{
						CallKey:        string(key.CallKey),
						StorageKey:     storageKey,
						Expiration:     expiration,
						PrevStorageKey: call.StorageKey,
					})
				}

			default:
				// We have a cached entry and it hasn't expired yet, use it
				storageKey = call.StorageKey
			}
		} else {
			slog.Error("failed to select call from cache", "err", err)
		}
	}

	ctx = ctxWithStorageKey(ctx, storageKey)

	if ctx.Value(cacheContextKey[K, V]{K(storageKey), c}) != nil {
		return nil, ErrCacheRecursiveCall
	}

	c.mu.Lock()
	if c.ongoingCalls == nil {
		c.ongoingCalls = make(map[callConcurrencyKeys]*result[K, V])
	}
	if c.completedCalls == nil {
		c.completedCalls = make(map[string]*resultList[K, V])
	}
	if c.completedCallsByContent == nil {
		c.completedCallsByContent = make(map[string]*resultList[K, V])
	}

	if lst, ok := c.completedCalls[storageKey]; ok {
		if res := lst.first(); res != nil {
			res.refCount++
			c.mu.Unlock()
			return &perCallResult[K, V]{
				result:   res,
				hitCache: true,
			}, nil
		}
	}

	if key.ContentDigestKey != "" {
		if lst, ok := c.completedCallsByContent[string(key.ContentDigestKey)]; ok {
			if res := lst.first(); res != nil {
				res.refCount++
				c.mu.Unlock()
				return &perCallResult[K, V]{
					result:                res,
					hitCache:              true,
					hitContentDigestCache: true,
				}, nil
			}
		}
	}

	var zeroKey K
	if key.ConcurrencyKey != zeroKey {
		if res, ok := c.ongoingCalls[callConcKeys]; ok {
			// already an ongoing call
			res.waiters++
			c.mu.Unlock()
			return c.wait(ctx, res, false)
		}
	}

	// make a new call with ctx that's only canceled when all caller contexts are canceled
	callCtx := context.WithValue(ctx, cacheContextKey[K, V]{K(storageKey), c}, struct{}{})
	callCtx, cancel := context.WithCancelCause(context.WithoutCancel(callCtx))
	res := &result[K, V]{
		cache: c,

		storageKey:          storageKey,
		callConcurrencyKeys: callConcKeys,

		persistToDB: persistToDB,

		waitCh:  make(chan struct{}),
		cancel:  cancel,
		waiters: 1,
	}

	if key.ConcurrencyKey != zeroKey {
		c.ongoingCalls[callConcKeys] = res
	}

	go func() {
		defer close(res.waitCh)
		valWithCallbacks, err := fn(callCtx)
		res.err = err
		if valWithCallbacks != nil {
			res.val = valWithCallbacks.Value
			res.postCall = valWithCallbacks.PostCall
			res.onRelease = valWithCallbacks.OnRelease
			res.safeToPersistCache = valWithCallbacks.SafeToPersistCache
			res.contentDigestKey = K(valWithCallbacks.ContentDigestKey)
			res.resultCallKey = K(valWithCallbacks.ResultCallKey)
		}
	}()

	c.mu.Unlock()
	perCallRes, err := c.wait(ctx, res, true)
	if err != nil {
		return nil, err
	}

	// ensure that this is never marked as hit cache, even in the case
	// where fn returned very quickly and was done by the time wait got
	// called
	perCallRes.hitCache = false
	return perCallRes, nil
}

func (c *cache) wait(ctx context.Context, res *result, isFirstCaller bool) (*perCallResult[K, V], error) {
	var hitCache bool
	var err error

	// first check just if the call is done already, if it is we consider it a cache hit
	select {
	case <-res.waitCh:
		hitCache = true
		err = res.err
	default:
		// call wasn't done in fast path check, wait for either the call to
		// be done or the caller's ctx to be canceled
		select {
		case <-res.waitCh:
			err = res.err
		case <-ctx.Done():
			err = context.Cause(ctx)
		}
	}

	c.mu.Lock()

	res.waiters--
	if res.waiters == 0 {
		// no one else is waiting, can cancel the callCtx
		res.cancel(err)
	}

	if err == nil {
		delete(c.ongoingCalls, res.callConcurrencyKeys)
		lst, ok := c.completedCalls[res.storageKey]
		if ok {
			if existing := lst.first(); existing != nil {
				res = existing
			} else {
				lst.add(res)
			}
		} else {
			lst = newResultList[K, V]()
			lst.add(res)
			c.completedCalls[res.storageKey] = lst
		}

		if res.resultCallKey != "" && res.resultCallKey != K(res.storageKey) {
			resultKey := string(res.resultCallKey)
			lst := c.completedCalls[resultKey]
			if lst == nil {
				lst = newResultList[K, V]()
				c.completedCalls[resultKey] = lst
			}
			lst.add(res)
		}

		if res.contentDigestKey != "" {
			contentKey := string(res.contentDigestKey)
			lst := c.completedCallsByContent[contentKey]
			if lst == nil {
				lst = newResultList[K, V]()
				c.completedCallsByContent[contentKey] = lst
			}
			lst.add(res)
		}

		res.refCount++
		c.mu.Unlock()

		if isFirstCaller && res.persistToDB != nil && res.safeToPersistCache {
			err := res.persistToDB(ctx)
			if err != nil {
				slog.Error("failed to persist cache expiration", "err", err)
			}
		}

		return &perCallResult[K, V]{
			result:   res,
			hitCache: hitCache,
		}, nil
	}

	if res.refCount == 0 && res.waiters == 0 {
		// error happened and no refs left, delete it now
		delete(c.ongoingCalls, res.callConcurrencyKeys)
		if lst := c.completedCalls[res.storageKey]; lst != nil {
			lst.remove(res)
			if lst.empty() {
				delete(c.completedCalls, res.storageKey)
			}
		}
	}

	c.mu.Unlock()
	return nil, err
}

func (res *result) Result() V {
	if res == nil {
		var zero V
		return zero
	}
	return res.val
}

func (res *result) Release(ctx context.Context) error {
	if res == nil || res.cache == nil {
		// wasn't cached, nothing to do
		return nil
	}

	res.cache.mu.Lock()
	res.refCount--
	var onRelease OnReleaseFunc
	if res.refCount == 0 && res.waiters == 0 {
		// no refs left and no one waiting on it, delete from cache
		delete(res.cache.ongoingCalls, res.callConcurrencyKeys)
		if lst := res.cache.completedCalls[res.storageKey]; lst != nil {
			lst.remove(res)
			if lst.empty() {
				delete(res.cache.completedCalls, res.storageKey)
			}
		}
		if res.resultCallKey != "" && res.resultCallKey != K(res.storageKey) {
			key := string(res.resultCallKey)
			if lst := res.cache.completedCalls[key]; lst != nil {
				lst.remove(res)
				if lst.empty() {
					delete(res.cache.completedCalls, key)
				}
			}
		}
		if res.contentDigestKey != "" {
			key := string(res.contentDigestKey)
			if lst := res.cache.completedCallsByContent[key]; lst != nil {
				lst.remove(res)
				if lst.empty() {
					delete(res.cache.completedCallsByContent, key)
				}
			}
		}
		onRelease = res.onRelease
	}
	res.cache.mu.Unlock()

	if onRelease != nil {
		return onRelease(ctx)
	}
	return nil
}

func (res *result) PostCall(ctx context.Context) error {
	if res.postCall == nil {
		return nil
	}
	return res.postCall(ctx)
}
