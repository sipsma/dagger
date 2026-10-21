package core

// Spike9 is a generic immutable tool-key -> File map over the ordinary DagQL
// result cache. The mutexes coordinate publication only; they store no entries.
import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine"
	"github.com/dagger/dagger/util/hashutil"
	"github.com/opencontainers/go-digest"
)

type Spike9Key struct{ Provider, Toolchain, Key string }

var spike9PublishLocks [256]sync.Mutex
var errSpike9Miss = errors.New("spike9 cache miss")

func (key Spike9Key) frame() (*dagql.ResultCall, error) {
	frame := &dagql.ResultCall{Kind: dagql.ResultCallKindField, Field: "_spike9EntryV1", Type: dagql.NewResultCallType((&File{}).Type())}
	for _, arg := range []struct{ name, value string }{{"provider", key.Provider}, {"toolchain", key.Toolchain}, {"key", key.Key}} {
		if arg.value == "" || len(arg.value) > 4096 {
			return nil, fmt.Errorf("spike9 %s must have 1..4096 bytes", arg.name)
		}
		frame.Args = append(frame.Args, &dagql.ResultCallArg{Name: arg.name, Value: &dagql.ResultCallLiteral{Kind: dagql.ResultCallLiteralKindString, StringValue: arg.value}})
	}
	return frame, nil
}

func spike9Call(ctx context.Context, key Spike9Key, makeFile func(context.Context, *dagql.ResultCall) (dagql.AnyResult, error)) (dagql.ObjectResult[*File], bool, error) {
	frame, err := key.frame()
	if err != nil {
		return dagql.ObjectResult[*File]{}, false, err
	}
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return dagql.ObjectResult[*File]{}, false, err
	}
	md, err := engine.ClientMetadataFromContext(ctx)
	if err != nil {
		return dagql.ObjectResult[*File]{}, false, err
	}
	srv := dagql.CurrentDagqlServer(ctx)
	// No concurrency key: a miss must not join (and poison) an arriving put.
	// Puts are serialized separately. Errors from this callback are not cached.
	res, err := cache.GetOrInitCall(ctx, md.SessionID, srv, &dagql.CallRequest{ResultCall: frame, IsPersistable: true}, func(ctx context.Context) (dagql.AnyResult, error) {
		if makeFile == nil {
			return nil, errSpike9Miss
		}
		return makeFile(ctx, frame)
	})
	if errors.Is(err, errSpike9Miss) {
		return dagql.ObjectResult[*File]{}, false, nil
	}
	if err != nil {
		return dagql.ObjectResult[*File]{}, false, err
	}
	value, ok := res.(dagql.ObjectResult[*File])
	if !ok {
		return value, false, fmt.Errorf("unexpected spike9 entry %T", res)
	}
	return value, true, nil
}

func Spike9Get(ctx context.Context, key Spike9Key) (dagql.ObjectResult[*File], bool, error) {
	return spike9Call(ctx, key, nil)
}

// The entry's File owns an independent immutable snapshot reference, with no
// host/filesync recipe or local path handle dependency. Normal snapshot owners,
// persistence, transfer and GC apply. File name/metadata are part of identity.
func Spike9Put(ctx context.Context, key Spike9Key, source dagql.ObjectResult[*File]) (dagql.ObjectResult[*File], error) {
	if _, err := key.frame(); err != nil {
		return dagql.ObjectResult[*File]{}, err
	}
	if len(source.Self().Services) != 0 {
		return dagql.ObjectResult[*File]{}, fmt.Errorf("spike9 cannot publish a service-dependent file")
	}
	sourceDigest, err := spike9FileDigest(ctx, source)
	if err != nil {
		return dagql.ObjectResult[*File]{}, err
	}
	sum := sha256.Sum256([]byte(key.Provider + "\x00" + key.Toolchain + "\x00" + key.Key))
	lock := &spike9PublishLocks[sum[0]]
	lock.Lock()
	defer lock.Unlock()
	result, _, err := spike9Call(ctx, key, func(ctx context.Context, frame *dagql.ResultCall) (dagql.AnyResult, error) {
		fresh, err := cloneDetachedFileForContainerResult(ctx, source.Self())
		if err != nil {
			return nil, err
		}
		// Copy no lazy recipe: a cached tool assertion is authoritative until GC.
		obj, err := dagql.NewObjectResultForCall(fresh, dagql.CurrentDagqlServer(ctx), frame)
		if err != nil {
			fresh.OnRelease(context.WithoutCancel(ctx))
			return nil, err
		}
		obj, err = obj.WithContentDigest(ctx, sourceDigest)
		if err != nil {
			fresh.OnRelease(context.WithoutCancel(ctx))
			return nil, err
		}
		return obj, nil
	})
	if err != nil {
		return result, err
	}
	published, err := result.ResultCall()
	if err != nil {
		return result, err
	}
	if published.ContentDigest() != sourceDigest {
		return result, fmt.Errorf("spike9 conflicting publication for provider/toolchain/key")
	}
	return result, nil
}

func spike9FileDigest(ctx context.Context, file dagql.ObjectResult[*File]) (digest.Digest, error) {
	// Evaluation before detaching ensures a complete owned snapshot, not a recipe
	// referencing a mutable client-side source. Include public name and platform.
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return "", err
	}
	if err := cache.Evaluate(ctx, file); err != nil {
		return "", err
	}
	checksum, err := file.Self().Digest(ctx, file, false)
	if err != nil {
		return "", err
	}
	path, err := file.Self().PathOrEval(ctx, file)
	if err != nil {
		return "", err
	}
	return hashutil.HashStrings("spike9-file-v1", filepath.Base(path), fmt.Sprint(file.Self().Platform), checksum), nil
}
