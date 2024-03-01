package idproto

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"hash"
	"strconv"
	sync "sync"

	"github.com/moby/locker"
	"github.com/opencontainers/go-digest"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/zeebo/xxh3"
	"google.golang.org/protobuf/proto"
)

func New() *ID {
	// we start with nil so there's always a nil parent at the bottom
	return nil
}

func (id *ID) Inputs() ([]digest.Digest, error) {
	seen := map[digest.Digest]struct{}{}
	var inputs []digest.Digest
	for _, arg := range id.Args {
		ins, err := arg.Value.Inputs()
		if err != nil {
			return nil, err
		}
		for _, in := range ins {
			if _, ok := seen[in]; ok {
				continue
			}
			seen[in] = struct{}{}
			inputs = append(inputs, in)
		}
	}
	return inputs, nil
}

func (lit *Literal) Inputs() ([]digest.Digest, error) {
	switch x := lit.Value.(type) {
	case *Literal_Id:
		dig, err := x.Id.Digest()
		if err != nil {
			return nil, err
		}
		return []digest.Digest{dig}, nil
	case *Literal_List:
		var inputs []digest.Digest
		for _, v := range x.List.Values {
			ins, err := v.Inputs()
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, ins...)
		}
		return inputs, nil
	case *Literal_Object:
		var inputs []digest.Digest
		for _, v := range x.Object.Values {
			ins, err := v.Value.Inputs()
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, ins...)
		}
		return inputs, nil
	default:
		return nil, nil
	}
}

func (id *ID) Modules() []*Module {
	allMods := []*Module{}
	for id != nil {
		if id.Module != nil {
			allMods = append(allMods, id.Module)
		}
		for _, arg := range id.Args {
			allMods = append(allMods, arg.Value.Modules()...)
		}
		id = id.Base
	}
	seen := map[digest.Digest]struct{}{}
	deduped := []*Module{}
	for _, mod := range allMods {
		dig, err := mod.Id.Digest()
		if err != nil {
			panic(err)
		}
		if _, ok := seen[dig]; ok {
			continue
		}
		seen[dig] = struct{}{}
		deduped = append(deduped, mod)
	}
	return deduped
}

func (id *ID) Path() string {
	buf := new(bytes.Buffer)
	if id.Base != nil {
		fmt.Fprintf(buf, "%s.", id.Base.Path())
	}
	fmt.Fprint(buf, id.DisplaySelf())
	return buf.String()
}

func (id *ID) DisplaySelf() string {
	buf := new(bytes.Buffer)
	fmt.Fprintf(buf, "%s", id.Field)
	for ai, arg := range id.Args {
		if ai == 0 {
			fmt.Fprintf(buf, "(")
		} else {
			fmt.Fprintf(buf, ", ")
		}
		fmt.Fprintf(buf, "%s: %s", arg.Name, arg.Value.Display())
		if ai == len(id.Args)-1 {
			fmt.Fprintf(buf, ")")
		}
	}
	if id.Nth != 0 {
		fmt.Fprintf(buf, "#%d", id.Nth)
	}
	return buf.String()
}

func (id *ID) Display() string {
	return fmt.Sprintf("%s: %s", id.Path(), id.Type.ToAST())
}

func (id *ID) WithNth(i int) *ID {
	cp := id.Clone()
	cp.Nth = int64(i)
	return cp
}

func (id *ID) SelectNth(i int) {
	id.Nth = int64(i)
	id.Type = id.Type.Elem
}

func (id *ID) Append(ret *ast.Type, field string, args ...*Argument) *ID {
	var tainted bool
	for _, arg := range args {
		if arg.Tainted() {
			tainted = true
			break
		}
	}

	return &ID{
		Base:    id,
		Type:    NewType(ret),
		Field:   field,
		Args:    args,
		Tainted: tainted,
	}
}

func (id *ID) Rebase(root *ID) *ID {
	cp := id.Clone()
	rebase(cp, root)
	return cp
}

func rebase(id *ID, root *ID) {
	if id.Base == nil {
		id.Base = root
	} else {
		rebase(id.Base, root)
	}
}

func (id *ID) SetTainted(tainted bool) {
	id.Tainted = tainted
}

// Tainted returns true if the ID contains any tainted selectors.
func (id *ID) IsTainted() bool {
	if id.Tainted {
		return true
	}
	if id.Base != nil {
		return id.Base.IsTainted()
	}
	return false
}

// Canonical returns the ID with any contained IDs canonicalized.
func (id *ID) Canonical() *ID {
	if id.Meta {
		return id.Base.Canonical()
	}
	canon := id.Clone()
	if id.Base != nil {
		canon.Base = id.Base.Canonical()
	}
	// TODO sort args...? is it worth preserving them in the first place? (default answer no)
	for i, arg := range canon.Args {
		canon.Args[i] = arg.Canonical()
	}
	return canon
}

var digestCache *sync.Map

// EnableDigestCache enables caching of digests for IDs.
//
// This is not thread-safe and should be called before any IDs are created, or
// at least digested.
// TODO: safe to rm now?
func EnableDigestCache() {
	digestCache = new(sync.Map)
}

// TODO: global vars are wrong, but it feels so right
var idDigestMu = locker.New()

// Digest returns the digest of the encoded ID. It does NOT canonicalize the ID
// first.
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// TODO:
// This is technically different than the previous digest, which included all
// proto fields... Either add the rest of the fields or make this a separate
// method and restore the old one
func (id *ID) Digest() (digest.Digest, error) {
	if id == nil {
		return "", nil
	}

	lockid := fmt.Sprintf("%v", id)
	idDigestMu.Lock(lockid)
	defer idDigestMu.Unlock(lockid)
	if id.PrecomputedDigest != "" {
		return digest.Digest(id.PrecomputedDigest), nil
	}

	h := xxh3.New()
	baseDgst, err := id.Base.Digest()
	if err != nil {
		return "", fmt.Errorf("failed to digest base ID: %w", err)
	}
	if _, err := h.Write([]byte(baseDgst)); err != nil {
		return "", fmt.Errorf("failed to write base ID digest: %w", err)
	}
	if _, err := h.Write([]byte(id.Field)); err != nil {
		return "", fmt.Errorf("failed to write field: %w", err)
	}
	if id.Nth != 0 {
		if _, err := h.Write([]byte(strconv.Itoa(int(id.Nth)))); err != nil {
			return "", fmt.Errorf("failed to write nth: %w", err)
		}
	}
	for _, arg := range id.Args {
		if _, err := h.Write([]byte(arg.Name)); err != nil {
			return "", fmt.Errorf("failed to write arg name: %w", err)
		}
		if err := arg.Value.digestInto(h); err != nil {
			return "", fmt.Errorf("failed to digest arg value: %w", err)
		}
	}

	id.PrecomputedDigest = fmt.Sprintf("xxh3:%x", h.Sum(nil))
	d := digest.Digest(id.PrecomputedDigest)
	return d, nil
}

func (lit *Literal) digestInto(h hash.Hash) error {
	switch x := lit.Value.(type) {
	case *Literal_Id:
		dgst, err := x.Id.Digest()
		if err != nil {
			return fmt.Errorf("failed to digest ID: %w", err)
		}
		if _, err := h.Write([]byte(dgst.String())); err != nil {
			return fmt.Errorf("failed to write ID digest: %w", err)
		}
	case *Literal_Null:
		if _, err := h.Write([]byte(strconv.FormatBool(x.Null))); err != nil {
			return fmt.Errorf("failed to write null: %w", err)
		}
	case *Literal_Bool:
		if _, err := h.Write([]byte(strconv.FormatBool(x.Bool))); err != nil {
			return fmt.Errorf("failed to write bool: %w", err)
		}
	case *Literal_Enum:
		if _, err := h.Write([]byte(x.Enum)); err != nil {
			return fmt.Errorf("failed to write enum: %w", err)
		}
	case *Literal_Int:
		if _, err := h.Write([]byte(strconv.FormatInt(x.Int, 10))); err != nil {
			return fmt.Errorf("failed to write int: %w", err)
		}
	case *Literal_Float:
		if _, err := h.Write([]byte(strconv.FormatFloat(x.Float, 'g', -1, 64))); err != nil {
			return fmt.Errorf("failed to write float: %w", err)
		}
	case *Literal_String_:
		if _, err := h.Write([]byte(x.String_)); err != nil {
			return fmt.Errorf("failed to write string: %w", err)
		}
	case *Literal_List:
		for _, v := range x.List.Values {
			if err := v.digestInto(h); err != nil {
				return fmt.Errorf("failed to digest list value: %w", err)
			}
		}
	case *Literal_Object:
		for _, v := range x.Object.Values {
			if _, err := h.Write([]byte(v.Name)); err != nil {
				return fmt.Errorf("failed to write object name: %w", err)
			}
			if err := v.Value.digestInto(h); err != nil {
				return fmt.Errorf("failed to digest object value: %w", err)
			}
		}
	default:
		// better to be slightly inefficient than to error out from a missing case
		bytes, err := proto.Marshal(lit)
		if err != nil {
			return fmt.Errorf("failed to marshal literal: %w", err)
		}
		if _, err := h.Write(bytes); err != nil {
			return fmt.Errorf("failed to write literal: %w", err)
		}
	}

	return nil
}

func (id *ID) Clone() *ID {
	return proto.Clone(id).(*ID)
}

func (id *ID) Encode() (string, error) {
	proto, err := proto.Marshal(id)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(proto), nil
}

func (id *ID) Decode(str string) error {
	bytes, err := base64.URLEncoding.DecodeString(str)
	if err != nil {
		return fmt.Errorf("cannot decode ID from %q: %w", str, err)
	}
	return proto.Unmarshal(bytes, id)
}

func (id *ID) TypeName() string {
	var typeName string
	elem := id.Type.Elem
	for typeName == "" {
		if elem == nil {
			break
		}
		typeName = elem.NamedType
		elem = elem.Elem
	}
	return typeName
}

// Canonical returns the argument with any contained IDs canonicalized.
func (arg *Argument) Canonical() *Argument {
	return &Argument{
		Name:  arg.Name,
		Value: arg.GetValue().Canonical(),
	}
}

// Tainted returns true if the ID contains any tainted selectors.
func (arg *Argument) Tainted() bool {
	return arg.GetValue().Tainted()
}
