package jobs

import (
	"context"
	"sync"
	"time"
)

// resettableDeadlineContext is the context passed to a deadline-bearing job.
// Its timer deadline can be moved forward without the deadline of the source
// context cancelling it first. The source is still watched for explicit
// cancellation; only its DeadlineExceeded result is ignored.
type resettableDeadlineContext struct {
	values  context.Context
	signal  context.Context
	done    chan struct{}
	timer   *time.Timer
	stopSig func() bool

	mu       sync.Mutex
	deadline time.Time
	err      error
}

func newResettableDeadlineContext(values, signal context.Context, deadline time.Time) *resettableDeadlineContext {
	c := &resettableDeadlineContext{
		values:   values,
		signal:   signal,
		done:     make(chan struct{}),
		deadline: deadline,
	}
	c.timer = time.AfterFunc(time.Until(deadline), c.expire)
	if signal.Done() != nil {
		c.stopSig = context.AfterFunc(signal, func() {
			err := signal.Err()
			if err != nil && err != context.DeadlineExceeded {
				c.cancel(err)
			}
		})
	}
	if err := signal.Err(); err != nil && err != context.DeadlineExceeded {
		c.cancel(err)
	}
	return c
}

func (c *resettableDeadlineContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deadline, true
}

func (c *resettableDeadlineContext) Done() <-chan struct{} { return c.done }

func (c *resettableDeadlineContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *resettableDeadlineContext) Value(key any) any { return c.values.Value(key) }

// extend moves the deadline by duration. The caller must have already checked
// the request and owns the registry lock; this method serializes with the
// timer callback so an extension cannot resurrect an expired context.
func (c *resettableDeadlineContext) extend(duration time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || !time.Now().Before(c.deadline) {
		return false
	}
	c.deadline = c.deadline.Add(duration)
	c.timer.Reset(time.Until(c.deadline))
	return true
}

func (c *resettableDeadlineContext) expire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil || time.Now().Before(c.deadline) {
		return
	}
	c.err = context.DeadlineExceeded
	close(c.done)
}

func (c *resettableDeadlineContext) cancel(err error) bool {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return false
	}
	c.err = err
	c.timer.Stop()
	close(c.done)
	stopSig := c.stopSig
	c.stopSig = nil
	c.mu.Unlock()
	if stopSig != nil {
		stopSig()
	}
	return true
}
