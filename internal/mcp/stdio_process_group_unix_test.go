//go:build !windows

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidAlive reports whether pid names a live process, using signal 0 (which
// only checks existence/permissions, it doesn't actually deliver a signal).
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// TestStdioClientCloseKillsWholeProcessGroup covers item 4: killing only the
// direct child orphans a wrapper's real worker (the way "npx <server>"
// orphans the "node" it execs). The fake server here backgrounds a "sleep"
// well outside the direct child, and then waits on it -- so the shell
// process only exits once the background job does. If Close only signals
// the direct child (the old behavior), the parent shell dies but the
// backgrounded "sleep" is left running, orphaned, exactly like a leaked
// "node" process. Setpgid + signaling the whole group (this fix) must kill
// it too.
func TestStdioClientCloseKillsWholeProcessGroup(t *testing.T) {
	requireSh(t)

	pidFile := filepath.Join(t.TempDir(), "child.pid")

	// Write the init response, background a long sleep (recording its pid),
	// then wait on it so this shell only exits once its child does -- which
	// forces Close past the grace period into the force-kill path while the
	// backgrounded child is still alive and sharing this process's pgid.
	// Unlike initScript, this does not block on draining stdin: nothing
	// here would notice stdin closing, which is exactly what forces Close
	// into its force-kill path instead of the "process exited on its own"
	// path.
	script := `printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}}\n'
sleep 60 &
echo $! > ` + pidFile + `
wait`

	c := NewStdioClient("fake", "sh", []string{"-c", script})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	// Wait for the child to record its background sleep's pid.
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			s := strings.TrimSpace(string(data))
			if s != "" {
				pid, perr := strconv.Atoi(s)
				if perr == nil {
					childPID = pid
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s to be written", pidFile)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !pidAlive(childPID) {
		t.Fatalf("backgrounded sleep (pid %d) was not alive before Close()", childPID)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close() did not return within 5s")
	}

	// Give the killed process a brief moment to actually be reaped by the
	// OS/its zombie state to clear, then check it's gone.
	deadline = time.Now().Add(2 * time.Second)
	for pidAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	if pidAlive(childPID) {
		t.Errorf("backgrounded sleep (pid %d) is still alive after Close(); expected the whole process group to be killed", childPID)
	}
}
