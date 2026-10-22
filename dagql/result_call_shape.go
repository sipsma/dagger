package dagql

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/opencontainers/go-digest"
	"github.com/zeebo/xxh3"

	"github.com/dagger/dagger/util/hashutil"
)

// callShapeArgLimit bounds a call's per-position literal digests
// (telemetryattrs.CacheArgDigestsAttr): with more literals, the last position
// folds every literal from it on.
const callShapeArgLimit = 32

// callShape is a call's shape, its literal values masked, and the digests of
// those literals by position.
type callShape struct {
	// digest is the xxh3 digest of the shape.
	digest digest.Digest
	// args are the literal digests in pre-order, 16 lowercase hex characters
	// each, at most callShapeArgLimit of them: with more literals, the last
	// folds every literal from its position on. nil when the call has no
	// literals.
	args []string
	// count is the number of literals.
	count int
}

// callShapeOf derives frame's shape and literal digests in one walk. The shape
// covers the result type, the field, the receiver's type, the module's name,
// the list selection and the view, and the arguments in order: each one's
// name, and its value's kind, a list's length and an object's field names,
// recursively. It masks every literal: bools, numbers, strings, bytes and enum
// values, list elements and object fields included, each of which is instead
// one position of args. Result refs and digest witnesses are structural
// inputs, so only their kind counts, and sensitive arguments are redacted
// first, as for the self digest. Implicit inputs are left out. Two calls with
// the same shape have the same literal positions.
func callShapeOf(frame *ResultCall) (callShape, error) {
	field, err := resultCallIdentityField(frame)
	if err != nil {
		return callShape{}, err
	}
	if frame.Type == nil {
		return callShape{}, fmt.Errorf("result call frame %q: missing type", field)
	}
	w := callShapeWalker{h: hashutil.NewHasher()}
	w.h = appendResultCallTypeBytes(w.h, frame.Type).WithDelim()
	w.h = w.h.WithString(field).WithDelim()
	if frame.Receiver != nil {
		w.h = w.h.WithByte('r')
		receiver := frame.Receiver.Call
		if receiver == nil {
			receiver = frame.Receiver.loadSharedCall()
		}
		if receiver != nil {
			w.h = appendResultCallTypeBytes(w.h, receiver.Type)
		}
	}
	w.h = w.h.WithDelim()
	if frame.Module != nil {
		w.h = w.h.WithByte('m').WithString(frame.Module.Name)
	}
	w.h = w.h.WithDelim()
	w.h = w.h.WithInt64(frame.Nth).WithDelim()
	w.h = w.h.WithString(frame.View.String()).WithDelim()
	for _, arg := range frame.Args {
		arg = redactedCallArgForDigest(arg)
		if arg == nil {
			continue
		}
		if err := w.arg(arg); err != nil {
			w.h.Close()
			return callShape{}, fmt.Errorf("result call frame %q args: %w", field, err)
		}
	}
	return callShape{
		digest: digest.Digest(w.h.DigestAndClose()),
		args:   w.argDigests(),
		count:  w.count,
	}, nil
}

// callShapeWalker accumulates a call's shape and its literal digests.
type callShapeWalker struct {
	h *hashutil.Hasher
	// leaves are the first callShapeArgLimit literal digests. Past them, fold
	// hashes the big-endian digests of every literal from the last position
	// on, in order.
	leaves [callShapeArgLimit]uint64
	fold   xxh3.Hasher
	count  int
}

func (w *callShapeWalker) arg(arg *ResultCallArg) error {
	w.h = w.h.WithString(arg.Name)
	if err := w.literal(arg.Value); err != nil {
		return fmt.Errorf("argument %q: %w", arg.Name, err)
	}
	w.h = w.h.WithDelim()
	return nil
}

func (w *callShapeWalker) literal(lit *ResultCallLiteral) error {
	var scratch [8]byte
	switch {
	case lit == nil || lit.Kind == ResultCallLiteralKindNull:
		w.h = w.h.WithByte('1')
	case lit.Kind == ResultCallLiteralKindResultRef:
		w.h = w.h.WithByte('0')
	case lit.Kind == ResultCallLiteralKindDigestedString:
		w.h = w.h.WithByte('9')
	case lit.Kind == ResultCallLiteralKindBool:
		w.h = w.h.WithByte('2')
		if lit.BoolValue {
			scratch[0] = 1
		}
		w.leaf('2', xxh3.Hash(scratch[:1]))
	case lit.Kind == ResultCallLiteralKindEnum:
		w.h = w.h.WithByte('3')
		w.leaf('3', xxh3.HashString(lit.EnumValue))
	case lit.Kind == ResultCallLiteralKindInt:
		w.h = w.h.WithByte('4')
		binary.BigEndian.PutUint64(scratch[:], uint64(lit.IntValue))
		w.leaf('4', xxh3.Hash(scratch[:]))
	case lit.Kind == ResultCallLiteralKindFloat:
		w.h = w.h.WithByte('5')
		binary.BigEndian.PutUint64(scratch[:], math.Float64bits(lit.FloatValue))
		w.leaf('5', xxh3.Hash(scratch[:]))
	case lit.Kind == ResultCallLiteralKindString:
		w.h = w.h.WithByte('6')
		w.leaf('6', xxh3.HashString(lit.StringValue))
	case lit.Kind == ResultCallLiteralKindBytes:
		w.h = w.h.WithByte('A')
		w.leaf('A', xxh3.Hash(lit.BytesValue))
	case lit.Kind == ResultCallLiteralKindList:
		w.h = w.h.WithByte('7').WithInt64(int64(len(lit.ListItems)))
		for _, elem := range lit.ListItems {
			if err := w.literal(elem); err != nil {
				return err
			}
		}
	case lit.Kind == ResultCallLiteralKindObject:
		w.h = w.h.WithByte('8').WithInt64(int64(len(lit.ObjectFields)))
		for _, field := range lit.ObjectFields {
			if err := w.arg(field); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown result call frame literal kind %q", lit.Kind)
	}
	w.h = w.h.WithDelim()
	return nil
}

// leaf records one literal's digest, given the xxh3 of its value's bytes:
// the xxh3 of that hash, big-endian, followed by the literal's kind byte, so
// equal bytes of different kinds differ.
func (w *callShapeWalker) leaf(kind byte, value uint64) {
	var buf [9]byte
	binary.BigEndian.PutUint64(buf[:8], value)
	buf[8] = kind
	dig := xxh3.Hash(buf[:])
	last := len(w.leaves) - 1
	switch {
	case w.count < len(w.leaves):
		w.leaves[w.count] = dig
	case w.count == len(w.leaves):
		// The first literal past the bound: the last position folds from
		// here on, starting with the literal it held.
		w.fold.Reset()
		binary.BigEndian.PutUint64(buf[:8], w.leaves[last])
		_, _ = w.fold.Write(buf[:8])
		fallthrough
	default:
		binary.BigEndian.PutUint64(buf[:8], dig)
		_, _ = w.fold.Write(buf[:8])
	}
	w.count++
}

// argDigests formats the literal digests as one hex string, sliced per
// position.
func (w *callShapeWalker) argDigests() []string {
	if w.count == 0 {
		return nil
	}
	n := min(w.count, callShapeArgLimit)
	buf := make([]byte, 16*n)
	var raw [8]byte
	for i := range n {
		v := w.leaves[i]
		if i == len(w.leaves)-1 && w.count > len(w.leaves) {
			v = w.fold.Sum64()
		}
		binary.BigEndian.PutUint64(raw[:], v)
		hex.Encode(buf[16*i:], raw[:])
	}
	all := string(buf)
	out := make([]string, n)
	for i := range out {
		out[i] = all[16*i : 16*(i+1)]
	}
	return out
}
