package dagql

import (
	"context"
	"sync"
)

// A remote push sends the Cloud the records of a result the engine computed,
// as the result is retained: storing a record is not a decision, so the
// engine doesn't wait for the service to ask for it. The cache queues each
// such result here when its retention edge is created; the integration takes
// the queue and pushes it (engine/remotecache), and puts back what it
// couldn't push yet. The queue lives on the bridge, so it survives the
// integration's reconnections.

// remotePushes is one bridge's push queue. Its mutex is a leaf: it is never
// held while taking another lock.
type remotePushes struct {
	mu     sync.Mutex
	closed bool
	// queued are the results waiting, by number, in the order they were
	// retained; in is the same set, so that a result waits once.
	queued []uint64
	in     map[uint64]struct{}
	// notify holds a signal when results were queued since the last take.
	notify chan struct{}
	// done closes when the bridge detaches.
	done chan struct{}
}

func (p *remotePushes) init() {
	p.in = map[uint64]struct{}{}
	p.notify = make(chan struct{}, 1)
	p.done = make(chan struct{})
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
		select {
		case p.notify <- struct{}{}:
		default:
		}
	}
}

// close refuses later use, and wakes the consumer.
func (p *remotePushes) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	p.queued, p.in = nil, nil
	close(p.done)
}

// TakeRemotePushes waits until a result is queued for a push, and takes every
// result queued, in the order they were retained. Only the integration's
// pusher takes.
func (b *RemoteCacheBridge) TakeRemotePushes(ctx context.Context) ([]uint64, error) {
	p := &b.pushes
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrRemoteCacheBridgeClosed
		}
		if len(p.queued) > 0 {
			numbers := p.queued
			p.queued = nil
			clear(p.in)
			p.mu.Unlock()
			return numbers, nil
		}
		p.mu.Unlock()
		select {
		case <-p.notify:
		case <-p.done:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// RequeueRemotePushes puts back results the pusher took and didn't push, as
// when its connection ended: they go ahead of those queued since, in their
// order, so that a result is pushed after the results it was retained after.
func (b *RemoteCacheBridge) RequeueRemotePushes(numbers []uint64) {
	p := &b.pushes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(numbers) == 0 {
		return
	}
	var back []uint64
	for _, number := range numbers {
		if _, ok := p.in[number]; ok {
			continue
		}
		p.in[number] = struct{}{}
		back = append(back, number)
	}
	p.queued = append(back, p.queued...)
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// RemotePushesQueued reports whether any result waits to be taken.
func (b *RemoteCacheBridge) RemotePushesQueued() bool {
	p := &b.pushes
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queued) > 0
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
