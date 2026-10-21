package core

// Experimental transport only. Enabled on the spike's fresh dev engine;
// deliberately not an SDK/API proposal or a production cache contract.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/dagger/dagger/dagql"
	bkcache "github.com/dagger/dagger/engine/snapshots"
	"github.com/dagger/dagger/util/hashutil"
)

const spike5Output = "/.spike5/output"
const spike5Token = "__spike5_sealed_file__"

func Spike5Enabled() bool { return os.Getenv("_DAGGER_SPIKE5") == "1" }

// Spike5FileView is a bulk read-only bind view, with no copied archive bytes.
// It intentionally exposes the cost of N ordinary bind mounts, not a FUSE or
// overlay implementation. The resulting container owns reopened snapshot refs.
func Spike5FileView(ctx context.Context, parent dagql.ObjectResult[*Container], files []dagql.ObjectResult[*File], paths []string) (_ *Container, rerr error) {
	if !Spike5Enabled() {
		return nil, fmt.Errorf("spike5 disabled")
	}
	if len(files) != len(paths) {
		return nil, fmt.Errorf("spike5: files/paths length mismatch")
	}
	if err := spike5ValidatePaths(paths); err != nil {
		return nil, err
	}
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return nil, err
	}
	inputs := make([]dagql.AnyResult, len(files))
	for i := range files {
		inputs[i] = files[i]
	}
	if err := cache.Evaluate(ctx, inputs...); err != nil {
		return nil, err
	}
	ctr := &Container{}
	if err := materializeContainerStateFromParent(ctx, ctr, parent); err != nil {
		return nil, err
	}
	defer func() {
		if rerr != nil {
			_ = ctr.OnRelease(context.WithoutCancel(ctx))
		}
	}()
	for _, m := range ctr.Mounts {
		if strings.HasPrefix(m.Target, "/.spike5") {
			return nil, fmt.Errorf("spike5: reserved mount exists")
		}
	}
	for i, file := range files {
		detached, err := cloneDetachedFileForContainerResult(ctx, file.Self())
		if err != nil {
			return nil, err
		}
		source := new(LazyAccessor[*File, *Container])
		source.setValue(detached)
		ctr.Mounts = append(ctr.Mounts, ContainerMount{Target: paths[i], Readonly: true, FileSource: source})
		ctr.Services.Merge(file.Self().Services)
	}
	ctr.ImageRef = ""
	return ctr, nil
}

func spike5ValidatePaths(paths []string) error {
	seen := map[string]bool{}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/.spike5/view/") || path.Clean(p) != p || seen[p] {
			return fmt.Errorf("spike5: invalid or duplicate view path %q", p)
		}
		seen[p] = true
	}
	for p := range seen {
		for d := path.Dir(p); d != "/"; d = path.Dir(d) {
			if seen[d] {
				return fmt.Errorf("spike5: overlapping view paths")
			}
		}
	}
	return nil
}

// Reserved argument conventions replace a new pragma and SDK generation in
// this spike. The flags and original File/Directory IDs remain call inputs.
func spike5BindRuntime(ctx context.Context, ctr dagql.ObjectResult[*Container], fn *FunctionCall) (dagql.ObjectResult[*Container], error) {
	if !Spike5Enabled() {
		return ctr, nil
	}
	args := map[string]json.RawMessage{}
	for _, a := range fn.InputArgs {
		args[a.Name] = a.Value.Bytes()
	}
	var bind, output bool
	if b := args["spike5Bind"]; b != nil {
		if err := json.Unmarshal(b, &bind); err != nil {
			return ctr, err
		}
	}
	if b := args["spike5Output"]; b != nil {
		if err := json.Unmarshal(b, &output); err != nil {
			return ctr, err
		}
	}
	if !bind && !output {
		return ctr, nil
	}
	srv, err := CurrentDagqlServer(ctx)
	if err != nil {
		return ctr, err
	}
	selectMount := func(field, target string, source dagql.Input, readonly bool) error {
		var next dagql.ObjectResult[*Container]
		err := srv.Select(ctx, ctr, &next, dagql.Selector{Field: field, Args: []dagql.NamedInput{
			{Name: "path", Value: dagql.String(target)}, {Name: "source", Value: source}, {Name: "readOnly", Value: dagql.Boolean(readonly)},
		}})
		if err == nil {
			ctr = next
		}
		return err
	}
	if bind {
		for _, item := range []struct{ name, target, field string }{
			{"spike5Dir", "/.spike5/input", "withMountedDirectory"},
			{"spike5File", "/.spike5/file", "withMountedFile"},
		} {
			raw := args[item.name]
			if len(raw) == 0 || string(raw) == "null" {
				continue
			}
			var id string
			if err := json.Unmarshal(raw, &id); err != nil {
				return ctr, err
			}
			var input dagql.Input
			if item.name == "spike5Dir" {
				input, err = (dagql.ID[*Directory]{}).DecodeInput(id)
			} else {
				input, err = (dagql.ID[*File]{}).DecodeInput(id)
			}
			if err != nil {
				return ctr, err
			}
			if err := selectMount(item.field, item.target, input, true); err != nil {
				return ctr, err
			}
		}
	}
	if output {
		var dir dagql.ObjectResult[*Directory]
		if err := srv.Select(ctx, srv.Root(), &dir, dagql.Selector{Field: "directory"}); err != nil {
			return ctr, err
		}
		id, err := ResultIDInput(dir)
		if err != nil {
			return ctr, err
		}
		if err := selectMount("withMountedDirectory", spike5Output, id, false); err != nil {
			return ctr, err
		}
	}
	cache, err := dagql.EngineCache(ctx)
	if err != nil {
		return ctr, err
	}
	if err := cache.Evaluate(ctx, ctr); err != nil {
		return ctr, err
	}
	return ctr, nil
}

// Called only after Sync has waited for the process and committed its writable
// mounts. ReturnValue merely stores a token; it never waits for this capture.
func spike5CaptureOutput(ctx context.Context, ctr *Container, fn *FunctionCall) error {
	if !Spike5Enabled() {
		return nil
	}
	r, set, err := fn.returnResult()
	if err != nil {
		return err
	}
	token, _ := r.Value.(string)
	if !set || r.HasError || token != spike5Token {
		return nil
	}
	for _, m := range ctr.Mounts {
		if m.Target != spike5Output {
			continue
		}
		dir, ok := m.DirectorySource.Peek()
		if !ok {
			return fmt.Errorf("spike5: uncommitted output")
		}
		ref, ok := dir.Snapshot.Peek()
		if !ok || ref == nil {
			return fmt.Errorf("spike5: missing output snapshot")
		}
		if err := MountRef(ctx, ref, func(root string, _ *mount.Mount) error {
			st, err := os.Lstat(root + "/result")
			if err != nil {
				return err
			}
			if !st.Mode().IsRegular() {
				return fmt.Errorf("spike5: output must be a regular file")
			}
			return nil
		}); err != nil {
			return err
		}
		query, err := CurrentQuery(ctx)
		if err != nil {
			return err
		}
		owned, err := query.SnapshotManager().GetBySnapshotID(ctx, ref.SnapshotID(), bkcache.NoUpdateLastUsed)
		if err != nil {
			return err
		}
		file := &File{Platform: ctr.Platform, File: new(LazyAccessor[string, *File]), Snapshot: new(LazyAccessor[bkcache.ImmutableRef, *File])}
		file.SetPath("/result")
		file.SetSnapshot(owned)
		fn.spike5File = file
		return nil
	}
	return fmt.Errorf("spike5: sealed return requires spike5Output=true")
}

func spike5FileResult(ctx context.Context, file *File) (_ dagql.AnyResult, rerr error) {
	defer func() {
		if rerr != nil {
			_ = file.OnRelease(context.WithoutCancel(ctx))
		}
	}()
	srv, err := CurrentDagqlServer(ctx)
	if err != nil {
		return nil, err
	}
	ref, _ := file.Snapshot.Peek()
	dgst, err := GetContentHashFromFile(ctx, ref, "/result")
	if err != nil {
		return nil, err
	}
	result, err := dagql.NewObjectResultForCurrentCall(ctx, srv, file)
	if err != nil {
		return nil, err
	}
	return result.WithContentDigest(ctx, hashutil.HashStrings("/result", string(dgst)))
}
