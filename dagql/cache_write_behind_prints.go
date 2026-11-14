package dagql

import (
	"encoding/binary"
	"fmt"
	"hash/maphash"
	"reflect"

	persistdb "github.com/dagger/dagger/dagql/persistdb"
)

// A write behind rewrites an entry the store holds a table at a time: of
// its rows but its dependencies, which are written by difference, only the
// tables whose rows changed since the store's were written. The writer
// keeps a fingerprint of the rows each table holds for each saved entry, and
// of each written term's rows, and compares the rows a write would write
// against them. A print of zero is unknown, as after a restore or a last-use
// update, and its rows are written.

// entryPrints are the fingerprints of a saved entry's rows in the store, one
// for each table but its dependencies, and one for those (depsPrint): an
// entry whose dependencies didn't change has them left alone, unread.
type entryPrints [depsPrint + 1]uint64

const depsPrint = int(persistdb.MirrorResultTables)

// rowPrinter fingerprints rows: each a struct, or a slice of them, whose
// fields are integers, booleans, strings and byte slices.
type rowPrinter struct {
	h   maphash.Hash
	buf [8]byte
}

func newRowPrinter(seed maphash.Seed) *rowPrinter {
	p := &rowPrinter{}
	p.h.SetSeed(seed)
	return p
}

// print is the fingerprint of rows, never zero.
func (p *rowPrinter) print(rows any) uint64 {
	p.h.Reset()
	p.value(reflect.ValueOf(rows))
	return p.h.Sum64() | 1
}

func (p *rowPrinter) uint(v uint64) {
	binary.LittleEndian.PutUint64(p.buf[:], v)
	_, _ = p.h.Write(p.buf[:])
}

func (p *rowPrinter) value(v reflect.Value) {
	switch v.Kind() {
	case reflect.Int, reflect.Int64, reflect.Int32:
		p.uint(uint64(v.Int()))
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		p.uint(v.Uint())
	case reflect.Bool:
		if v.Bool() {
			p.uint(1)
		} else {
			p.uint(0)
		}
	case reflect.String:
		p.uint(uint64(v.Len()))
		_, _ = p.h.WriteString(v.String())
	case reflect.Slice:
		p.uint(uint64(v.Len()))
		if v.Type().Elem().Kind() == reflect.Uint8 {
			_, _ = p.h.Write(v.Bytes())
			return
		}
		for i := range v.Len() {
			p.value(v.Index(i))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			p.value(v.Field(i))
		}
	case reflect.Pointer:
		if v.IsNil() {
			p.uint(0)
			return
		}
		p.uint(1)
		p.value(v.Elem())
	default:
		panic(fmt.Sprintf("write behind: no fingerprint for a row's %s", v.Type()))
	}
}

// printEntry fingerprints the rows a write would write for an entry, but its
// dependencies: edge is its persisted edge, nil for none.
func (p *rowPrinter) printEntry(rows *persistResultRows, edge *persistdb.MirrorPersistedEdge) entryPrints {
	var out entryPrints
	out[persistdb.MirrorResultsTable] = p.print(rows.result.row)
	out[persistdb.MirrorResultOutputEqClassesTable] = p.print(rows.outputs)
	out[persistdb.MirrorResultCallsTable] = p.print(rows.calls)
	out[persistdb.MirrorResultTaughtTable] = p.print(rows.taught)
	out[persistdb.MirrorResultSnapshotLinksTable] = p.print(rows.result.resultSnapshotLinks)
	out[persistdb.MirrorPersistedEdgesTable] = p.print(edge)
	out[depsPrint] = p.print(rows.result.resultDeps)
	return out
}

// printTerm fingerprints a term's rows: the term and its inputs.
func (p *rowPrinter) printTerm(rows *persistTermRows) uint64 {
	return p.print(*rows)
}
