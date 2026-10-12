package tools

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/locks"
)

// TestLockSurvivesCallCtx: a lock without a TTL lives until the agent ends.
// The call that took it returns, and its context ends, but the lock stays.
func TestLockSurvivesCallCtx(t *testing.T) {
	reg := locks.NewRegistry()
	agent, endAgent := context.WithCancel(context.Background())
	defer endAgent()

	call, endCall := context.WithCancel(agent)
	res := (&LockTool{Registry: reg}).Run(WithAgentCtx(call, agent), map[string]any{"path": "/x/file.go"})
	if !res.Success {
		t.Fatalf("lock failed: %s", res.Error)
	}
	endCall()
	time.Sleep(50 * time.Millisecond)
	if _, locked := reg.IsLocked("/x/file.go"); !locked {
		t.Fatal("lock was dropped when the call context ended; it must live until the agent ends")
	}

	endAgent()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, locked := reg.IsLocked("/x/file.go"); !locked {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lock was not released after the agent ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMutationFIFOOrder: mutations of one worktree run in the order they
// start, and a write waits while the FIFO is held.
func TestMutationFIFOOrder(t *testing.T) {
	wd := t.TempDir()
	ctx := WithWorkdir(context.Background(), wd)

	holder := AcquireMutation(ctx)
	if holder == nil {
		t.Fatal("first acquire must succeed")
	}

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel := AcquireMutation(ctx)
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			rel()
		}()
		time.Sleep(30 * time.Millisecond) // fixed arrival order
	}

	path := filepath.Join(wd, "f.txt")
	writeDone := make(chan ToolResult, 1)
	go func() {
		writeDone <- (&WriteTool{}).Run(ctx, map[string]any{"path": path, "content": "x"})
	}()
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("write ran while the worktree FIFO was held")
	}

	holder()
	wg.Wait()
	if res := <-writeDone; !res.Success {
		t.Fatalf("write failed: %s", res.Error)
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if i >= len(order) || order[i] != want[i] {
			t.Fatalf("grant order = %v, want %v", order, want)
		}
	}
}

// TestBashParallelSkipsFIFO: bash with parallel=true runs while a mutation
// holds the worktree FIFO; without it, the bash call waits.
func TestBashParallelSkipsFIFO(t *testing.T) {
	ctx := WithWorkdir(context.Background(), t.TempDir())
	holder := AcquireMutation(ctx)
	defer holder()

	done := make(chan ToolResult, 1)
	go func() {
		done <- (&BashTool{}).Run(ctx, map[string]any{"command": "echo parallel-ok", "parallel": true})
	}()
	select {
	case res := <-done:
		if !res.Success {
			t.Fatalf("parallel bash failed: %s", res.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bash parallel=true waited for the worktree FIFO")
	}

	waiting := make(chan ToolResult, 1)
	go func() {
		waiting <- (&BashTool{}).Run(ctx, map[string]any{"command": "echo serial"})
	}()
	select {
	case <-waiting:
		t.Fatal("bash without parallel ran while the FIFO was held")
	case <-time.After(150 * time.Millisecond):
	}
}
