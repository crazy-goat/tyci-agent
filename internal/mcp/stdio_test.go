package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
	"sync"
	"testing"
	"time"
)

// requireSh skips the test if there's no "sh" on this platform to drive the
// fake MCP server scripts below.
func requireSh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available on this platform")
	}
}

// initScript is a minimal fake MCP server: it writes one valid JSON-RPC
// initialize response (id 1, matching StdioClient's first nextID) to
// stdout, then keeps draining stdin until it's closed so that
// notifications/initialized (and Close's stdin.Close) don't blow up on a
// broken pipe.
const initScript = `printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}}\n'; cat >/dev/null`

// TestStdioClientInitializeHandshake performs a real stdio handshake against
// a script that speaks the protocol. Before the Initialize locking fix,
// Initialize takes c.mu and holds it for the whole call, then sendRequest
// tries to take c.mu again -- sync.Mutex is not reentrant, so this
// deadlocks forever. That deadlock is not tied to ctx, so we impose our own
// bounded wait via time.After and fail (rather than hang the suite) if
// Initialize doesn't return in time.
func TestStdioClientInitializeHandshake(t *testing.T) {
	requireSh(t)

	c := NewStdioClient("fake", "sh", []string{"-c", initScript})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	initDone := make(chan error, 1)
	go func() { initDone <- c.Initialize(ctx) }()

	select {
	case err := <-initDone:
		if err != nil {
			t.Fatalf("Initialize() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Initialize() did not return within 5s — likely deadlocked re-acquiring c.mu")
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}

// TestStdioClientCloseForceKillsHangingProcess exercises the case where the
// child does not exit when stdin closes (e.g. it forked off a real worker
// and the shell driving it doesn't care about EOF). Close must still return
// promptly by force-killing after the grace period, rather than blocking
// forever.
func TestStdioClientCloseForceKillsHangingProcess(t *testing.T) {
	requireSh(t)

	// Drain stdin (so writes/close don't error) but then ignore that EOF
	// and keep running well past any reasonable grace period.
	script := initScript + `; sleep 30`
	c := NewStdioClient("fake", "sh", []string{"-c", script})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	start := time.Now()
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s for a hanging process — grace-period force-kill isn't working")
	}

	elapsed := time.Since(start)
	if elapsed < closeGracePeriod {
		t.Errorf("Close() returned in %v, faster than the %v grace period; expected it to wait before force-killing", elapsed, closeGracePeriod)
	}
}

// TestStdioClientCloseWakesInFlightRequest covers F8's second nil-dereference:
// StdioClient.Close used to close every pending channel, after which an
// in-flight sendRequest received the zero value and returned (nil, nil),
// which CallTool then dereferenced via resp.Error. Here we start a CallTool
// that will never get a real response (the fake server never answers
// tools/call), close the client concurrently, and require CallTool to
// return an error -- not panic, not hang.
func TestStdioClientCloseWakesInFlightRequest(t *testing.T) {
	requireSh(t)

	c := NewStdioClient("fake", "sh", []string{"-c", initScript})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	type outcome struct {
		res    *CallToolResult
		err    error
		panicV interface{}
	}
	callDone := make(chan outcome, 1)

	go func() {
		var o outcome
		defer func() {
			// A panic here (e.g. a nil-pointer dereference on resp.Error)
			// must fail the test loudly rather than being folded into a
			// generic error -- that would let the exact F8 regression this
			// test targets slip through as a "pass".
			o.panicV = recover()
			callDone <- o
		}()
		o.res, o.err = c.CallTool(ctx, "whatever", json.RawMessage(`{}`))
	}()

	// Wait until the goroutine has actually registered itself as a pending
	// request before closing the client out from under it. A fixed sleep
	// here would be a false-green risk: if Close ran before sendRequest
	// inserted into c.pending, CallTool would instead fail because it
	// wrote to an already-closed stdin -- a different error, from a
	// different code path, that would make this test pass without ever
	// exercising the closed-channel wake it's named for.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		pending := len(c.pending)
		c.mu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for CallTool to register a pending request")
		}
		time.Sleep(time.Millisecond)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	select {
	case result := <-callDone:
		if result.panicV != nil {
			t.Fatalf("CallTool() panicked after Close(): %v", result.panicV)
		}
		if result.err == nil {
			t.Fatalf("CallTool() returned no error after Close (result=%v); expected an error instead of a nil response", result.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CallTool() did not return within 5s after Close()")
	}
}

// TestStdioClientCloseConcurrentCallsDoNotRace covers batch 2's new
// constraint: MCPToolRunner.Close (tools/mcp.go) closes every client
// concurrently rather than serially under its own lock, so two goroutines
// can now legitimately both call the same StdioClient's Close() at once
// (e.g. a deferred shutdown racing a signal handler). Before closeOnce,
// both calls run the full body, including a second cmd.Wait() -- which
// exec.Cmd documents as unsafe to call more than once, since it reads and
// mutates cmd.ProcessState. This starts the hanging-process variant (so
// both calls actually reach the force-kill/cmd.Wait path rather than one
// finding the process already gone) from many goroutines at once and
// requires: no panic/race, both calls return the same error, and Close
// still returns within a bounded time.
func TestStdioClientCloseConcurrentCallsDoNotRace(t *testing.T) {
	requireSh(t)

	script := initScript + `; sleep 30`
	c := NewStdioClient("fake", "sh", []string{"-c", script})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	const n = 8
	errs := make([]error, n)
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.Close()
		}(i)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Close() calls did not all return within 5s")
	}

	for i, err := range errs {
		if err != nil {
			t.Errorf("Close() call %d returned error: %v", i, err)
		}
	}
}
