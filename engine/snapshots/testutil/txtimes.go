package testutil

import (
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

// EngineBoltOptions are the options the engine opens its containerd metadata
// database with (engine/server/server.go, initBoltDBs).
func EngineBoltOptions() *bolt.Options {
	return &bolt.Options{
		NoSync:         true,
		NoFreelistSync: true,
		FreelistType:   bolt.FreelistMapType,
		NoStatistics:   true,
		NoGrowSync:     true,
	}
}

// Root is the directory the store keeps its content, snapshots and metadata
// database in.
func (s *Store) Root() string {
	return s.root
}

// TxTimes records the write transactions on a store's metadata database. bbolt
// runs one write transaction at a time, so a transaction's wait is the time
// other transactions held the database.
type TxTimes struct {
	mu  sync.Mutex
	txs []TxTime
}

// TxTime is one write transaction: the function that opened it, how long it
// waited to begin, and how long it held the database, its commit included.
type TxTime struct {
	Caller string
	Start  time.Time
	Wait   time.Duration
	Hold   time.Duration
}

// Since returns the transactions that started at or after start.
func (r *TxTimes) Since(start time.Time) []TxTime {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []TxTime
	for _, tx := range r.txs {
		if !tx.Start.Before(start) {
			out = append(out, tx)
		}
	}
	return out
}

func (r *TxTimes) add(tx TxTime) {
	r.mu.Lock()
	r.txs = append(r.txs, tx)
	r.mu.Unlock()
}

type timedTransactor struct {
	*bolt.DB
	times *TxTimes
}

func (t timedTransactor) Update(fn func(*bolt.Tx) error) error {
	caller := txCaller()
	start := time.Now()
	var began time.Time
	err := t.DB.Update(func(tx *bolt.Tx) error {
		began = time.Now()
		return fn(tx)
	})
	if !began.IsZero() {
		t.times.add(TxTime{Caller: caller, Start: start, Wait: began.Sub(start), Hold: time.Since(began)})
	}
	return err
}

// txCaller names the first function above the metadata database's own
// transaction helpers, for example containerd's
// metadata.(*namespacedWriter).Commit or metadata.(*snapshotter).Commit.
func txCaller() string {
	pcs := make([]uintptr, 16)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		name := frame.Function
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if !slices.Contains([]string{"metadata.update", "metadata.(*DB).Update", "testutil.observedTransactor.Update"}, name) {
			return name
		}
		if !more {
			return name
		}
	}
}
