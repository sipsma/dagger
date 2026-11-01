package wcprof

import (
	"context"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Holders a recorded wait on an RWMutex can name in place of an op.
const (
	// HolderUnknown: the lock was changing hands, released and not yet
	// marked by its next taker, or held by readers the recorder didn't
	// count.
	HolderUnknown uint64 = math.MaxUint64
	// HolderReaders: the lock was held for reading.
	HolderReaders uint64 = math.MaxUint64 - 1
	// HolderDirect is set in the holder of a lock taken directly (Lock or
	// TryLock), with no context to name its op: the holder's low 32 bits
	// are the recorder's string ID of the function that took it
	// (DirectHolderName).
	HolderDirect uint64 = 1 << 62
)

// DirectHolderName is the string ID of the function that took a lock
// directly, when holder is such a holder.
func DirectHolderName(holder uint64) (uint32, bool) {
	if holder == HolderUnknown || holder == HolderReaders || holder&HolderDirect == 0 {
		return 0, false
	}
	return uint32(holder), true
}

// RWMutex is a sync.RWMutex that remembers who holds it while a recorder
// is active, so that a wait on it recorded by WriteLock or ReadLock names
// its holder: the op that took it through WriteLock, the function that took
// it directly (HolderDirect), or HolderReaders while anyone reads it.
// Releasing it makes the holder unknown before the lock is free, so a
// waiter that reads it before the next taker marks itself reads
// HolderUnknown, never the holder that released it; and a waiter whose
// read overlaps a change of holder reads HolderUnknown too.
type RWMutex struct {
	sync.RWMutex
	holder atomic.Uint64
	since  atomic.Int64
	// seq counts changes of holder: odd while one is written (holderAt).
	seq atomic.Uint64
	// readers counts the readers that took it while a recorder was active.
	readers atomic.Int64
	// observe, when set, sees each holder an acquisition marks (ObserveMarks).
	observe atomic.Pointer[func(holder uint64)]
}

// Lock locks m for writing, held by its caller (markDirect).
//
//go:noinline
func (m *RWMutex) Lock() {
	m.RWMutex.Lock()
	m.markDirect()
}

// TryLock tries to lock m for writing, held by its caller (markDirect).
//
//go:noinline
func (m *RWMutex) TryLock() bool {
	if !m.RWMutex.TryLock() {
		return false
	}
	m.markDirect()
	return true
}

// Unlock unlocks m for writing.
func (m *RWMutex) Unlock() {
	m.release()
	m.RWMutex.Unlock()
}

// RLock locks m for reading.
func (m *RWMutex) RLock() {
	m.RWMutex.RLock()
	m.markReaders()
}

// TryRLock tries to lock m for reading.
func (m *RWMutex) TryRLock() bool {
	if !m.RWMutex.TryRLock() {
		return false
	}
	m.markReaders()
	return true
}

// RUnlock unlocks m for reading. The last reader to leave makes the holder
// unknown; so does a reader the recorder didn't count, which took m before
// it was active: unknown, not wrong.
func (m *RWMutex) RUnlock() {
	if global.Load() != nil {
		if m.leaveReaders() == 0 && m.holder.Load() == HolderReaders {
			m.set(HolderUnknown, 0)
		}
	}
	m.RWMutex.RUnlock()
}

// leaveReaders counts a reader leaving and returns the readers left, never
// below 0.
func (m *RWMutex) leaveReaders() int64 {
	for {
		n := m.readers.Load()
		if n <= 0 {
			return 0
		}
		if m.readers.CompareAndSwap(n, n-1) {
			return n - 1
		}
	}
}

// markDirect records the function that called Lock or TryLock as m's
// holder from now, while a recorder is active.
//
//go:noinline
func (m *RWMutex) markDirect() {
	r := global.Load()
	if r == nil {
		return
	}
	var pc [1]uintptr
	// Skip runtime.Callers, markDirect and Lock or TryLock.
	runtime.Callers(3, pc[:])
	holder := r.directHolder(pc[0])
	m.set(holder, r.Now())
	m.observed(holder)
}

// directHolder is the holder naming the function that called at pc, as
// package.function, looked up once per call site.
func (r *Recorder) directHolder(pc uintptr) uint64 {
	if h, ok := r.directHolders.Load(pc); ok {
		return h.(uint64)
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	name := frame.Function
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	h := HolderDirect | uint64(r.Intern(name))
	r.directHolders.Store(pc, h)
	return h
}

// ObserveMarks calls f, in the acquiring goroutine, with the holder each
// exclusive acquisition of m marks while a recorder is active, until the
// returned stop is called; for tests, which can tell from f's caller which
// code took m and whether it named its op.
func (m *RWMutex) ObserveMarks(f func(holder uint64)) (stop func()) {
	m.observe.Store(&f)
	return func() { m.observe.Store(nil) }
}

func (m *RWMutex) observed(holder uint64) {
	if f := m.observe.Load(); f != nil {
		(*f)(holder)
	}
}

// set changes m's holder and its time, seq odd while it does (holderAt).
// Only one holder sets them at a time, but readers: they set only
// HolderReaders or HolderUnknown with no time, so two at once can't mix
// another holder's time in.
func (m *RWMutex) set(holder uint64, since int64) {
	m.seq.Add(1)
	m.holder.Store(holder)
	m.since.Store(since)
	m.seq.Add(1)
}

// markReaders counts a reader and records m as held for reading, writing
// the holder only when it changes, so that readers don't contend on it.
// Readers have no time: one reader's would count the idle time before a
// later one as held.
func (m *RWMutex) markReaders() {
	if global.Load() == nil {
		return
	}
	m.readers.Add(1)
	if m.holder.Load() == HolderReaders {
		return
	}
	m.set(HolderReaders, 0)
}

// release makes m's holder unknown, before m is unlocked.
func (m *RWMutex) release() {
	if global.Load() == nil {
		return
	}
	m.set(HolderUnknown, 0)
}

// markOp records the op of ctx as m's holder, or WriteLock's caller when
// ctx names none, while a recorder is active; it looks nothing up
// otherwise, as its caller holds m.
//
//go:noinline
func (m *RWMutex) markOp(ctx context.Context) {
	r := global.Load()
	if r == nil {
		return
	}
	holder := CurrentOpID(ctx)
	if holder == 0 {
		var pc [1]uintptr
		// Skip runtime.Callers, markOp and WriteLock.
		runtime.Callers(3, pc[:])
		holder = r.directHolder(pc[0])
	}
	m.set(holder, r.Now())
	m.observed(holder)
}

// Holder is m's holder and when it took m, as a wait on m begun now would
// record them; 0 while no recorder has been active.
func (m *RWMutex) Holder() (uint64, int64) {
	return m.holderAt()
}

// holderAt reads m's holder and when it took m: HolderUnknown with no time
// when the holder changed while it read, even to another and back.
func (m *RWMutex) holderAt() (uint64, int64) {
	return m.holderAtWith(nil)
}

// holderAtWith is holderAt, running between, when set, after it has read
// the holder and its time; for tests.
func (m *RWMutex) holderAtWith(between func()) (uint64, int64) {
	seq := m.seq.Load()
	if seq%2 == 1 {
		return HolderUnknown, 0
	}
	h, since := m.holder.Load(), m.since.Load()
	if between != nil {
		between()
	}
	if m.seq.Load() != seq {
		return HolderUnknown, 0
	}
	return h, since
}

// RWLocker is a read-write lock: a sync.RWMutex, or an RWMutex.
type RWLocker interface {
	Lock()
	TryLock() bool
	RLock()
	TryRLock() bool
}
