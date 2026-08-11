package locks

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAcquireAndConflict(t *testing.T) {
	r := NewRegistry()

	release, ok, existing := r.Acquire(context.Background(), "/a/b", "agent-1", 0)
	if !ok || existing != nil {
		t.Fatalf("expected fresh acquire to succeed, got ok=%v existing=%v", ok, existing)
	}
	defer release()

	_, ok2, existing2 := r.Acquire(context.Background(), "/a/b", "agent-2", 0)
	if ok2 {
		t.Fatal("expected conflicting acquire to fail")
	}
	if existing2 == nil || existing2.Holder != "agent-1" {
		t.Fatalf("expected existing lock info for agent-1, got %+v", existing2)
	}
}

func TestIsLocked(t *testing.T) {
	r := NewRegistry()

	if _, ok := r.IsLocked("/x"); ok {
		t.Fatal("expected /x to be unlocked initially")
	}

	release, ok, _ := r.Acquire(context.Background(), "/x", "holder-1", 0)
	if !ok {
		t.Fatal("expected acquire to succeed")
	}
	defer release()

	l, ok := r.IsLocked("/x")
	if !ok || l.Holder != "holder-1" {
		t.Fatalf("expected /x locked by holder-1, got %+v ok=%v", l, ok)
	}
}

func TestTTLExpiry(t *testing.T) {
	r := NewRegistry()

	_, ok, _ := r.Acquire(context.Background(), "/ttl", "holder-1", 20*time.Millisecond)
	if !ok {
		t.Fatal("expected acquire to succeed")
	}

	if _, locked := r.IsLocked("/ttl"); !locked {
		t.Fatal("expected lock to be active immediately")
	}

	time.Sleep(60 * time.Millisecond)

	if _, locked := r.IsLocked("/ttl"); locked {
		t.Fatal("expected lock to have expired")
	}

	// A new holder should now be able to acquire it.
	_, ok2, _ := r.Acquire(context.Background(), "/ttl", "holder-2", 0)
	if !ok2 {
		t.Fatal("expected acquire after expiry to succeed")
	}
}

func TestReleaseByCtxCancel(t *testing.T) {
	r := NewRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	_, ok, _ := r.Acquire(ctx, "/ctx", "holder-1", 0)
	if !ok {
		t.Fatal("expected acquire to succeed")
	}

	if _, locked := r.IsLocked("/ctx"); !locked {
		t.Fatal("expected lock to be active")
	}

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, locked := r.IsLocked("/ctx"); !locked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected lock to be released after ctx cancellation")
}

func TestExplicitReleaseStopsWatcher(t *testing.T) {
	r := NewRegistry()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	release, ok, _ := r.Acquire(ctx, "/rel", "holder-1", 0)
	if !ok {
		t.Fatal("expected acquire to succeed")
	}
	release()
	release() // idempotent

	if _, locked := r.IsLocked("/rel"); locked {
		t.Fatal("expected lock to be released")
	}

	// New holder can immediately acquire since old one released explicitly.
	_, ok2, _ := r.Acquire(context.Background(), "/rel", "holder-2", 0)
	if !ok2 {
		t.Fatal("expected acquire after explicit release to succeed")
	}
}

func TestReleaseHolderMismatchFails(t *testing.T) {
	r := NewRegistry()

	release, ok, _ := r.Acquire(context.Background(), "/mismatch", "holder-1", 0)
	if !ok {
		t.Fatal("expected acquire to succeed")
	}
	defer release()

	if r.Release("/mismatch", "holder-2") {
		t.Fatal("expected release by wrong holder to fail")
	}

	l, locked := r.IsLocked("/mismatch")
	if !locked || l.Holder != "holder-1" {
		t.Fatal("expected lock to remain held by holder-1")
	}

	if !r.Release("/mismatch", "holder-1") {
		t.Fatal("expected release by correct holder to succeed")
	}

	if _, locked := r.IsLocked("/mismatch"); locked {
		t.Fatal("expected lock to be gone after correct release")
	}
}

func TestReleaseUnknownPath(t *testing.T) {
	r := NewRegistry()
	if r.Release("/nope", "anyone") {
		t.Fatal("expected release of unknown path to fail")
	}
}

func TestConcurrentAcquireRace(t *testing.T) {
	r := NewRegistry()

	const n = 50
	var wg sync.WaitGroup
	successes := make([]bool, n)
	releases := make([]func(), n)

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			holder := fmt.Sprintf("holder-%d", i)
			release, ok, _ := r.Acquire(context.Background(), "/race", holder, 0)
			successes[i] = ok
			releases[i] = release
		}(i)
	}
	wg.Wait()

	count := 0
	for i, ok := range successes {
		if ok {
			count++
			releases[i]()
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one acquire to win among distinct holders, got %d", count)
	}
}
