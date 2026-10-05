package channels

import (
	"errors"
	"sync"
)

// maxPendingOutbound bounds the messages dispatched to one channel that its
// worker has not taken yet. A channel that cannot keep up drops what comes
// beyond it, rather than holding up the other channels.
const maxPendingOutbound = 512

// errOutboundBacklog is the failure of a message dropped because its
// channel has too many messages waiting.
var errOutboundBacklog = errors.New("too many outbound messages waiting for the channel")

// pushResult is what pendingQueue.push did with a message.
type pushResult int

const (
	pushQueued pushResult = iota
	pushFull
	pushStopped
)

// pendingQueue is a FIFO between the dispatcher and one channel's worker
// queue. push never blocks; forward moves the messages to the worker queue
// in order, waiting only for that channel.
type pendingQueue[M any] struct {
	mu     sync.Mutex
	items  []M
	notify chan struct{}
}

// signal returns the queue's wake-up channel. Caller must hold q.mu.
func (q *pendingQueue[M]) signal() chan struct{} {
	if q.notify == nil {
		q.notify = make(chan struct{}, 1)
	}
	return q.notify
}

// push appends msg unless the worker has stopped or too many messages wait.
func (q *pendingQueue[M]) push(msg M, stop <-chan struct{}) pushResult {
	select {
	case <-stop:
		return pushStopped
	default:
	}
	q.mu.Lock()
	if len(q.items) >= maxPendingOutbound {
		q.mu.Unlock()
		return pushFull
	}
	q.items = append(q.items, msg)
	notify := q.signal()
	q.mu.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return pushQueued
}

// pop removes the oldest message, if any.
func (q *pendingQueue[M]) pop() (M, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var zero M
	if len(q.items) == 0 {
		return zero, false
	}
	msg := q.items[0]
	q.items[0] = zero
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}
	return msg, true
}

// len returns how many messages wait.
func (q *pendingQueue[M]) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// forward moves messages to out in order until stop is closed.
func (q *pendingQueue[M]) forward(out chan<- M, stop <-chan struct{}) {
	q.mu.Lock()
	notify := q.signal()
	q.mu.Unlock()
	for {
		msg, ok := q.pop()
		if !ok {
			select {
			case <-notify:
				continue
			case <-stop:
				return
			}
		}
		select {
		case out <- msg:
		case <-stop:
			return
		}
	}
}
