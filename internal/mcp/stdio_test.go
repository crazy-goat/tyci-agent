package mcp

import (
	"context"
	"encoding/json"
	"os/exec"
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

// TestStdioClientCloseGracePeriod checks that Close does try to wait for a
// well-behaved process to exit on its own after stdin closes, rather than
// unconditionally SIGKILLing it (the select{ default: kill } bug always
// took the default branch, since the cmd.Wait() goroutine could not
// possibly have sent yet). We can't easily observe "was it killed", but we
// can (and do) observe that Close returns promptly either way, and that a
// process which exits immediately on stdin-close doesn't need the grace
// period's full duration times a control script that hangs past it.
func TestStdioClientCloseGracePeriod(t *testing.T) {
	requireSh(t)

	c := NewStdioClient("fake", "sh", []string{"-c", initScript})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	start := time.Now()
	if err := c.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	elapsed := time.Since(start)

	// initScript's "cat" exits as soon as stdin is closed, so Close should
	// return quickly and well within the grace period, not just after it.
	if elapsed > closeGracePeriod {
		t.Errorf("Close() took %v, expected well under the %v grace period for a process that exits on stdin close", elapsed, closeGracePeriod)
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

	// Give the goroutine a moment to register itself as a pending request
	// before we close the client out from under it.
	time.Sleep(100 * time.Millisecond)

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
