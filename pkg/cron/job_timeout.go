package cron

import (
	"context"
	"sync"
	"time"
)

// jobContext is the context of one job run. It ends when its parent does,
// or once the run has used up the job timeout, which doesn't count the time
// the run spent waiting for the owner's approval (PauseJobTimeout). That
// moves the end, so the context reports no deadline of its own.
type jobContext struct {
	context.Context // the parent: values and deadline

	done chan struct{}

	mu         sync.Mutex
	err        error
	stopParent func() bool
	timer      *time.Timer
	left       time.Duration // the timeout left while the clock is stopped
	started    time.Time     // when the clock last started
	pauses     int
}

// jobTimerKey finds a run's jobContext from the contexts derived from it.
type jobTimerKey struct{}

// withJobTimeout returns a context for one run of a job, ended by its cancel
// function, by parent, or once the run has taken timeout.
func withJobTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	c := &jobContext{Context: parent, done: make(chan struct{}), left: timeout}
	c.mu.Lock()
	c.stopParent = context.AfterFunc(parent, func() { c.end(parent.Err()) })
	c.startClockLocked()
	c.mu.Unlock()
	return c, func() { c.end(context.Canceled) }
}

func (c *jobContext) Done() <-chan struct{} { return c.done }

func (c *jobContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *jobContext) Value(key any) any {
	if key == (jobTimerKey{}) {
		return c
	}
	return c.Context.Value(key)
}

// startClockLocked starts counting down the timeout left.
func (c *jobContext) startClockLocked() {
	c.started = time.Now()
	c.timer = time.AfterFunc(c.left, func() { c.end(context.DeadlineExceeded) })
}

// end ends the context with err, the first time only.
func (c *jobContext) end(err error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.err = err
	c.timer.Stop()
	stopParent := c.stopParent
	close(c.done)
	c.mu.Unlock()
	stopParent()
}

// pause stops the clock until the returned function is called. Pauses may
// overlap: the clock runs again when the last one ends.
func (c *jobContext) pause() (resume func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return func() {}
	}
	c.pauses++
	// Stop fails when the timeout just fired: the run ends anyway.
	if c.pauses == 1 && c.timer.Stop() {
		c.left -= time.Since(c.started)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.pauses--
			if c.pauses == 0 && c.err == nil {
				c.startClockLocked()
			}
		})
	}
}

// PauseJobTimeout stops counting the job timeout of the run ctx belongs to
// until the returned function is called: a run waiting for the owner's
// approval doesn't use up its time. Outside a job run it does nothing.
func PauseJobTimeout(ctx context.Context) (resume func()) {
	if ctx != nil {
		if c, ok := ctx.Value(jobTimerKey{}).(*jobContext); ok {
			return c.pause()
		}
	}
	return func() {}
}
