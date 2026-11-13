package dagql

import (
	"context"
	"sync"
)

// A remote push sends the Cloud the records of a result the engine computed,
// as the result is retained: storing a record is not a decision, so the
// engine doesn't wait for the service to ask for it. The cache queues each
// such result here when its retention edge is created; the integration's
// pusher takes the queue, pushes it (engine/remotecache), and says what
// became of each result it took: pushed or dropped (RemotePushesDone), put
// back (RequeueRemotePushes), or busy (WaitRemotePushesBusy). The queue lives
// on the bridge, so it survives the integration's reconnections, and it
// knows when every result queued has been pushed (RemotePushesIdle), which
// a settle waits for.

// remotePushes is one bridge's push queue. Its mutex is a leaf: it is never
// held while taking another lock.
type remotePushes struct {
	mu     sync.Mutex
	closed bool
	// queued are the results waiting, by number, in the order they were
	// retained, and busy those a push found busy, waiting for the next take
	// that has something else to push or is flushed. in is both sets, so
	// that a result waits once.
	queued []uint64
	busy   []uint64
	in     map[uint64]struct{}
	// taken counts the results a take handed out that the pusher hasn't
	// said the fate of yet.
	taken int
	// flush asks the next take to hand out the busy results even with
	// nothing queued.
	flush bool
	// notify holds a signal when there is something for a take.
	notify chan struct{}
	// changed closes, and is replaced, whenever the queue changes, for
	// RemotePushesIdle's waiters.
	changed chan struct{}
	// done closes when the bridge detaches.
	done chan struct{}
}

func (p *remotePushes) init() {
	p.in = map[uint64]struct{}{}
	p.notify = make(chan struct{}, 1)
	p.changed = make(chan struct{})
	p.done = make(chan struct{})
}

// changedLocked wakes the queue's waiters. Requires p.mu.
func (p *remotePushes) changedLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// signalLocked tells a take there is something for it. Requires p.mu.
func (p *remotePushes) signalLocked() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// add queues numbers that aren't waiting already, after those that are.
func (p *remotePushes) add(numbers ...uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	added := false
	for _, number := range numbers {
		if _, ok := p.in[number]; ok {
			continue
		}
		p.in[number] = struct{}{}
		p.queued = append(p.queued, number)
		added = true
	}
	if added {
		p.signalLocked()
		p.changedLocked()
	}
}

// close refuses later use, and wakes the pusher and the waiters.
func (p *remotePushes) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.queued, p.busy, p.in, p.taken = nil, nil, nil, 0
	close(p.done)
	p.changedLocked()
}

// TakeRemotePushes waits until a result is queued for a push, or the queue
// is flushed with busy results waiting, and takes every result waiting: the
// busy ones first, then those queued, in the order they were retained.
// flushed reports a take that a flush asked for. Only the integration's
// pusher takes, and it says the fate of each result it took.
func (b *RemoteCacheBridge) TakeRemotePushes(ctx context.Context) (numbers []uint64, flushed bool, err error) {
	p := &b.pushes
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, false, ErrRemoteCacheBridgeClosed
		}
		if len(p.queued) > 0 || p.flush && len(p.busy) > 0 {
			flushed = p.flush
			numbers = append(p.busy, p.queued...)
			p.busy, p.queued, p.flush = nil, nil, false
			clear(p.in)
			p.taken += len(numbers)
			p.mu.Unlock()
			return numbers, flushed, nil
		}
		p.flush = false
		p.mu.Unlock()
		select {
		case <-p.notify:
		case <-p.done:
		case <-ctx.Done():
			return nil, false, context.Cause(ctx)
		}
	}
}

// RemotePushesDone records that the pusher pushed, or dropped, n of the
// results it took.
func (b *RemoteCacheBridge) RemotePushesDone(n int) {
	p := &b.pushes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || n == 0 {
		return
	}
	p.taken -= n
	p.changedLocked()
}

// RequeueRemotePushes puts back results the pusher took and didn't push, as
// when its connection ended: they go ahead of those queued since, in their
// order, so that a result is pushed after the results it was retained after.
func (b *RemoteCacheBridge) RequeueRemotePushes(numbers []uint64) {
	b.pushes.putBack(numbers, false)
}

// WaitRemotePushesBusy puts back results a push found busy: they wait for a
// take that has something else to push, or that a flush asks for.
func (b *RemoteCacheBridge) WaitRemotePushesBusy(numbers []uint64) {
	b.pushes.putBack(numbers, true)
}

func (p *remotePushes) putBack(numbers []uint64, busy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(numbers) == 0 {
		return
	}
	p.taken -= len(numbers)
	var back []uint64
	for _, number := range numbers {
		if _, ok := p.in[number]; ok {
			continue
		}
		p.in[number] = struct{}{}
		back = append(back, number)
	}
	if busy {
		p.busy = append(p.busy, back...)
	} else {
		p.queued = append(back, p.queued...)
		p.signalLocked()
	}
	p.changedLocked()
}

// FlushRemotePushes asks the pusher to push the busy results once more, as
// a settle does: no session is live, so nothing will start that would make
// a take otherwise.
func (b *RemoteCacheBridge) FlushRemotePushes() {
	p := &b.pushes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.busy) == 0 {
		return
	}
	p.flush = true
	p.signalLocked()
}

// RemotePushesIdle reports whether every result queued has been pushed or
// dropped: none waits, busy or not, and the pusher holds none. changed
// closes when that may have changed.
func (b *RemoteCacheBridge) RemotePushesIdle() (idle bool, changed <-chan struct{}) {
	p := &b.pushes
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed || len(p.queued) == 0 && len(p.busy) == 0 && p.taken == 0, p.changed
}

// notePushLocked queues res for a push to the Cloud as its retention edge is
// created, when a remote cache integration is attached: res is a result this
// engine computed, not one a merge brought (imported) or its image supplies,
// and not one bound to its session, which no other cache can use. Requires
// egraphMu for writing.
func (c *Cache) notePushLocked(res *sharedResult) {
	if c.blobBacked || res.imported || res.boundSession != "" {
		return
	}
	b := c.partContentSource.attachedBridge()
	if b == nil {
		return
	}
	if _, image := res.imageHoldingLocked(); image != nil {
		return
	}
	b.pushes.add(uint64(res.id))
}
