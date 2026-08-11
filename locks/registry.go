// Package locks implements an in-process, advisory (cooperative) registry
// of path locks. It does not touch the filesystem or prevent any process
// from actually reading/writing a path — it only lets cooperating callers
// (e.g. parallel subagents sharing this process) agree "don't touch this
// path while I'm holding it".
package locks

import (
	"context"
	"sync"
	"time"
)

// Lock describes a currently held (or recently held, pre-cleanup) path lock.
type Lock struct {
	Path       string
	Holder     string
	AcquiredAt time.Time
	ExpiresAt  time.Time // zero value = no TTL; lives until Release or ctx.Done()

	stop chan struct{} // closed on explicit release, to stop the ctx-watcher goroutine
}

// Registry is an in-memory, mutex-guarded map of path -> Lock.
type Registry struct {
	mu    sync.Mutex
	locks map[string]*Lock
}

// NewRegistry creates an empty lock registry.
func NewRegistry() *Registry {
	return &Registry{locks: make(map[string]*Lock)}
}

// expired reports whether l has a TTL that has passed. Must be called with
// r.mu held.
func expired(l *Lock, now time.Time) bool {
	return !l.ExpiresAt.IsZero() && now.After(l.ExpiresAt)
}

// removeLocked deletes path's entry and stops its ctx-watcher goroutine (if
// any) without racing a concurrent explicit Release. Must be called with
// r.mu held.
func (r *Registry) removeLocked(path string, l *Lock) {
	if cur, ok := r.locks[path]; ok && cur == l {
		delete(r.locks, path)
	}
	select {
	case <-l.stop:
		// already closed
	default:
		close(l.stop)
	}
}

// Acquire tries to lock path for holder. If path is already locked by a
// different holder and that lock has not expired, it fails and returns the
// existing lock so the caller can build an informative error message.
//
// If ttl > 0, the lock expires automatically after ttl (ExpiresAt is set)
// and is also cleaned up lazily by IsLocked/Acquire once it has passed.
//
// If ttl == 0, the lock carries no fixed expiry; instead a goroutine waits
// on ctx.Done() and releases the lock automatically when the caller's
// context ends — i.e. the lock lives exactly as long as the session/agent
// that created it.
//
// The returned release func is idempotent and safe to call multiple times
// (and safe to call after the lock has already expired or been replaced).
func (r *Registry) Acquire(ctx context.Context, path, holder string, ttl time.Duration) (release func(), ok bool, existing *Lock) {
	now := time.Now()

	r.mu.Lock()
	if cur, found := r.locks[path]; found {
		if cur.Holder == holder {
			// Same holder re-acquiring: refresh and hand back a working
			// release, rather than reporting a conflict against itself.
		} else if !expired(cur, now) {
			r.mu.Unlock()
			return func() {}, false, cur
		}
		// Expired (or same holder) — fall through and replace it below,
		// stopping any watcher goroutine tied to the stale entry first.
		r.removeLocked(path, cur)
	}

	l := &Lock{
		Path:       path,
		Holder:     holder,
		AcquiredAt: now,
		stop:       make(chan struct{}),
	}
	if ttl > 0 {
		l.ExpiresAt = now.Add(ttl)
	}
	r.locks[path] = l
	r.mu.Unlock()

	var once sync.Once
	release = func() {
		once.Do(func() {
			r.mu.Lock()
			r.removeLocked(path, l)
			r.mu.Unlock()
		})
	}

	if ttl == 0 {
		go func() {
			select {
			case <-ctx.Done():
				release()
			case <-l.stop:
			}
		}()
	}

	return release, true, nil
}

// IsLocked reports the current lock on path, if any. A lock whose TTL has
// passed is treated as unlocked and is removed from the registry as a side
// effect.
func (r *Registry) IsLocked(path string) (*Lock, bool) {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	l, ok := r.locks[path]
	if !ok {
		return nil, false
	}
	if expired(l, now) {
		r.removeLocked(path, l)
		return nil, false
	}
	return l, true
}

// Release removes the lock on path only if it is currently held by holder.
// Returns false if path isn't locked, is held by someone else, or has
// already expired.
func (r *Registry) Release(path, holder string) bool {
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	l, ok := r.locks[path]
	if !ok {
		return false
	}
	if expired(l, now) {
		r.removeLocked(path, l)
		return false
	}
	if l.Holder != holder {
		return false
	}
	r.removeLocked(path, l)
	return true
}
