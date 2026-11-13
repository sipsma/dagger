package remotecache

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
)

// pushTestBundle is a push of root over child: each row held unless full
// names it, as a capture for a push sends them.
func pushTestBundle(root, child uint64, full map[uint64]bool) (dagql.ValueBundle, dagql.ExportedRows) {
	rows := dagql.ExportedRows{Full: map[uint64]dagql.RowRevision{}, Pushed: map[uint64]dagql.RowRevision{}}
	bundle := dagql.ValueBundle{Version: 8, Roots: []dagql.TransferredRoot{{Ordinal: 1}}}
	for i, number := range []uint64{root, child} {
		row := dagql.TransferredValue{Ordinal: dagql.TransferOrdinal(i + 1), SenderNumber: number, Record: dagql.PersistedRecord{ResultID: number}}
		if full[number] {
			rows.Full[number] = dagql.RowRevision{}
		} else {
			row.Held = digest.FromString("recipe")
			rows.Pushed[number] = dagql.RowRevision{}
		}
		bundle.Values = append(bundle.Values, row)
	}
	return bundle, rows
}

// pushRecorder is a fake Push that captures root over child, and records
// the rows each capture was asked for.
type pushRecorder struct {
	mu    sync.Mutex
	asked []dagql.PushRows
	busy  bool
}

func (r *pushRecorder) push(ctx context.Context, roots []uint64, rows *dagql.PushRows, consume func(context.Context, Export) error) error {
	r.mu.Lock()
	r.asked = append(r.asked, *rows)
	busy := r.busy
	r.mu.Unlock()
	if busy {
		return consume(ctx, Export{Skipped: []protocol.SkippedRoot{{Number: roots[0], Reason: protocol.SkipBusy}}})
	}
	bundle, exported := pushTestBundle(roots[0], 100, rows.Full)
	return consume(ctx, Export{Bundle: &bundle, Rows: exported})
}

func (r *pushRecorder) rows() []dagql.PushRows {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]dagql.PushRows(nil), r.asked...)
}

func pushesIdle(q *fakePushQueue) bool {
	idle, _ := q.RemotePushesIdle()
	return idle
}

// A result the cache queues is pushed at once. The engine first asks which
// rows of its closure the Cloud lacks, naming each as a held row, then
// pushes the one the Cloud lacks in full and the other held. A held row the
// Cloud finds stale as it commits goes in full in one more push. Once the
// Cloud commits, the rows are known, and the queue is idle.
func TestPushSendsWhatTheCloudLacksInFull(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	recorder := &pushRecorder{}
	adapter.push = recorder.push
	s, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	check := conn.next(t)
	require.Equal(t, protocol.TypeCheckRows, check.Type)
	rows := decode[protocol.CheckRows](t, check).Rows
	require.Len(t, rows, 2)
	for _, row := range rows {
		require.NotEmpty(t, row.Held, "the engine names rows, and sends no record")
	}
	s.dispatch(reply(check.ID, protocol.TypeCheckedRows, protocol.CheckedRows{Lacks: []uint64{7}}))

	push := conn.next(t)
	require.Equal(t, protocol.TypePush, push.Type)
	require.NotZero(t, decode[protocol.Push](t, push).EngineTimeUnixNano, "the push says when its roots were retained")
	bundle := decode[protocol.Push](t, push).Bundle
	require.Empty(t, bundle.Values[0].Held, "the row the Cloud lacks goes in full")
	require.NotEmpty(t, bundle.Values[1].Held, "the row it has stays held")
	require.False(t, pushesIdle(adapter.pushes), "not idle until the Cloud commits")
	s.dispatch(reply(push.ID, protocol.TypePushed, protocol.Pushed{Stale: []uint64{100}}))

	again := conn.next(t)
	require.Equal(t, protocol.TypePush, again.Type)
	for _, row := range decode[protocol.Push](t, again).Bundle.Values {
		require.Empty(t, row.Held, "the stale row goes in full too")
	}
	require.Equal(t, []dagql.PushRows{{List: true}, {Full: map[uint64]bool{7: true}}, {Full: map[uint64]bool{7: true, 100: true}}}, recorder.rows())
	s.dispatch(reply(again.ID, protocol.TypePushed, protocol.Pushed{}))
	require.Eventually(t, func() bool { return pushesIdle(adapter.pushes) }, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, 2, s.known.Len(), "both rows are known once the push merged")
	conn.none(t)
}

// A settle waits until every result queued has been pushed and committed:
// the service settles with the records of the sessions it settles for.
func TestSettleWaitsForThePushesBeforeIt(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	adapter.push = (&pushRecorder{}).push
	s, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	check := conn.next(t)
	require.Equal(t, protocol.TypeCheckRows, check.Type)
	adapter.setSettle(SettleState{Period: 1, Sessions: []protocol.SettleSession{{ID: "sess-1"}}})
	conn.none(t)
	s.dispatch(reply(check.ID, protocol.TypeCheckedRows, protocol.CheckedRows{}))
	push := conn.next(t)
	require.Equal(t, protocol.TypePush, push.Type)
	conn.none(t)

	s.dispatch(reply(push.ID, protocol.TypePushed, protocol.Pushed{}))
	settle := conn.next(t)
	require.Equal(t, protocol.TypeSettle, settle.Type, "the settle goes once the push is committed")
}

// A result whose closure is busy waits for the next push, and a settle
// pushes it once more, since no session is live to finish what keeps it
// busy: one still busy then is dropped, and the settle goes.
func TestABusyPushIsTriedAgainAtTheSettleAndDropped(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	recorder := &pushRecorder{busy: true}
	adapter.push = recorder.push
	_, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	require.Eventually(t, func() bool { return len(recorder.rows()) == 1 }, 5*time.Second, 10*time.Millisecond)
	conn.none(t)
	require.False(t, pushesIdle(adapter.pushes), "the busy result still waits")

	adapter.setSettle(SettleState{Period: 1, Sessions: []protocol.SettleSession{{ID: "sess-1"}}})
	settle := conn.next(t)
	require.Equal(t, protocol.TypeSettle, settle.Type)
	require.Len(t, recorder.rows(), 2, "the settle tried the busy result once more")
	require.True(t, pushesIdle(adapter.pushes))
}

// A push the connection's end cuts off is put back for the next connection.
func TestAPushCutOffIsPutBack(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	adapter.push = (&pushRecorder{}).push
	s, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	require.Equal(t, protocol.TypeCheckRows, conn.next(t).Type)
	s.cancel(errors.New("connection lost"))
	require.Eventually(t, func() bool {
		adapter.pushes.mu.Lock()
		defer adapter.pushes.mu.Unlock()
		return len(adapter.pushes.queued) == 1 && adapter.pushes.queued[0] == 7 && adapter.pushes.taken == 0
	}, 5*time.Second, 10*time.Millisecond)
}

// A push in flight as a settle begins, which then finds its root busy, is
// tried once more by the settle and dropped, and the settle goes: the
// settle doesn't wait for something that will never come.
func TestASettleFlushesARootFoundBusyAfterItBegan(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	gate := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	adapter.push = func(ctx context.Context, roots []uint64, _ *dagql.PushRows, consume func(context.Context, Export) error) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			<-gate
		}
		return consume(ctx, Export{Skipped: []protocol.SkippedRoot{{Number: roots[0], Reason: protocol.SkipBusy}}})
	}
	_, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls == 1
	}, 5*time.Second, 10*time.Millisecond, "the push's capture is in flight")
	adapter.setSettle(SettleState{Period: 1, Sessions: []protocol.SettleSession{{ID: "sess-1"}}})
	conn.none(t)
	close(gate)

	settle := conn.next(t)
	require.Equal(t, protocol.TypeSettle, settle.Type, "the settle goes once the busy root is flushed and dropped")
	mu.Lock()
	require.Equal(t, 2, calls)
	mu.Unlock()
	require.True(t, pushesIdle(adapter.pushes))
}

// A root parked busy just after the settle's flush, which found none busy,
// still wakes the settle's wait: it subscribes to the queue's changes before
// it flushes. The settle flushes again, the root is dropped, and the settle
// goes.
func TestASettleSeesARootParkedBusyJustAfterItsFlush(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter()
	adapter.setSettle(SettleState{Period: 1, Live: true})
	gate := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	adapter.push = func(ctx context.Context, roots []uint64, _ *dagql.PushRows, consume func(context.Context, Export) error) error {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			<-gate
		}
		return consume(ctx, Export{Skipped: []protocol.SkippedRoot{{Number: roots[0], Reason: protocol.SkipBusy}}})
	}
	_, conn, _ := servedSession(t, adapter)

	adapter.pushes.add(7)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls == 1
	}, 5*time.Second, 10*time.Millisecond, "the push's capture is in flight")
	// The settle's first flush finds nothing busy; the push then parks its
	// root busy before the wait goes on.
	adapter.pushes.afterFlush = func() {
		close(gate)
		require.Eventually(t, func() bool { return adapter.pushes.busyLen() == 1 }, 5*time.Second, time.Millisecond)
	}
	adapter.setSettle(SettleState{Period: 1, Sessions: []protocol.SettleSession{{ID: "sess-1"}}})
	settle := conn.next(t)
	require.Equal(t, protocol.TypeSettle, settle.Type)
	require.True(t, pushesIdle(adapter.pushes))
}
