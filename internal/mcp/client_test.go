package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeTestConfig points ConfigPath (via $HOME) at a fresh temp directory
// containing the given mcp.json body, and restores HOME on cleanup
// (t.Setenv already does the restore).
func writeTestConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".tyci"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".tyci", "mcp.json"), []byte(body), 0600); err != nil {
		t.Fatalf("write mcp.json: %v", err)
	}
}

// mcpHTTPHandler answers the minimal handshake ConnectAllTimeout needs:
// initialize, notifications/initialized, tools/list. If gate is non-nil, it
// is read before the handler answers "initialize" — closing it (or never
// closing it) is how tests control exactly when/whether the server
// responds, without sleep-based timing.
func mcpHTTPHandler(t *testing.T, toolName string, gate <-chan struct{}) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			if gate != nil {
				<-gate
			}
			result := InitializeResult{ProtocolVersion: "2024-11-05", ServerInfo: ServerInfo{Name: "fake", Version: "1.0"}}
			resultJSON, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", Result: resultJSON, ID: req.ID})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			result := ToolListResult{Tools: []Tool{{Name: toolName, Description: "a fake tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
			resultJSON, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", Result: resultJSON, ID: req.ID})
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
		}
	}
}

func TestConnectAllTimeout_NoConfigFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // no .tyci/mcp.json under this HOME

	servers, err := ConnectAllTimeout(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected no servers, got %v", servers)
	}
}

// TestConnectAllTimeout_HealthyServer_ToolsAppear is the test that fails if
// ConnectAllTimeout (and so tools.InitMCP, and so the whole batch-2 wiring)
// is reverted to main's pre-batch-2 shape, a ConnectAll that returned only
// map[string]Client with no per-server tool list at all: it asserts the
// tools that came back alongside the client, which that older shape had no
// way to report.
func TestConnectAllTimeout_HealthyServer_ToolsAppear(t *testing.T) {
	server := httptest.NewServer(mcpHTTPHandler(t, "search", nil))
	defer server.Close()

	writeTestConfig(t, `{"mcpServers":{"good":{"url":"`+server.URL+`"}}}`)

	servers, err := ConnectAllTimeout(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := servers["good"]
	if !ok {
		t.Fatalf("expected server %q to connect, got %v", "good", servers)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "search" {
		t.Fatalf("expected tool %q, got %+v", "search", got.Tools)
	}
	got.Client.Close()
}

// TestConnectAllTimeout_MissingCommand_DoesNotBlockOrAppear covers "a
// server that fails to start does not prevent startup and does not appear":
// the command path is deliberately nonexistent, so exec never launches a
// real binary - Start() itself fails, exactly as it would for a typo'd or
// uninstalled server.
func TestConnectAllTimeout_MissingCommand_DoesNotBlockOrAppear(t *testing.T) {
	healthy := httptest.NewServer(mcpHTTPHandler(t, "search", nil))
	defer healthy.Close()

	writeTestConfig(t, `{"mcpServers":{
		"broken":{"command":"/nonexistent/path/tyci-test-should-not-exist-9f3a"},
		"good":{"url":"`+healthy.URL+`"}
	}}`)

	start := time.Now()
	servers, err := ConnectAllTimeout(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %s, expected the missing command to fail fast rather than consume its timeout budget", elapsed)
	}
	if _, ok := servers["broken"]; ok {
		t.Fatalf("did not expect the broken server to appear in the result")
	}
	good, ok := servers["good"]
	if !ok {
		t.Fatalf("expected the healthy server to connect despite the other one failing, got %v", servers)
	}
	good.Client.Close()
}

// TestConnectAllTimeout_HangingServer_BoundedByTimeout covers the bounded-
// timeout requirement directly: a server that never answers must not keep
// ConnectAllTimeout (and so startup) waiting past the given timeout, and
// must not appear in the result.
func TestConnectAllTimeout_HangingServer_BoundedByTimeout(t *testing.T) {
	gate := make(chan struct{})
	server := httptest.NewServer(mcpHTTPHandler(t, "search", gate))
	// server.Close() blocks until every outstanding request finishes, so
	// the gate must be released (unblocking the handler) BEFORE Close runs.
	// Deferred in this order, close(gate) — deferred second — runs first.
	defer server.Close()
	defer close(gate)

	writeTestConfig(t, `{"mcpServers":{"slow":{"url":"`+server.URL+`"}}}`)

	timeout := 100 * time.Millisecond
	start := time.Now()
	servers, err := ConnectAllTimeout(context.Background(), timeout)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("ConnectAllTimeout took %s for a %s timeout - startup would have hung", elapsed, timeout)
	}
	if _, ok := servers["slow"]; ok {
		t.Fatalf("did not expect the hanging server to appear in the result")
	}
}

func TestConnectAllTimeout_InvalidConfigFile_ReturnsError(t *testing.T) {
	writeTestConfig(t, `{ not valid json`)

	_, err := ConnectAllTimeout(context.Background(), time.Second)
	if err == nil {
		t.Fatalf("expected an error for an unparsable mcp.json")
	}
}

// mcpAuthHTTPHandler is like mcpHTTPHandler but records the Authorization
// header it saw on tools/list into seen (guarded by mu), so a test can
// verify each server actually got its own token rather than none, a
// mixed-up one, or a crash.
func mcpAuthHTTPHandler(mu *sync.Mutex, seen map[string]string, key, toolName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "initialize":
			result := InitializeResult{ProtocolVersion: "2024-11-05", ServerInfo: ServerInfo{Name: "fake", Version: "1.0"}}
			resultJSON, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", Result: resultJSON, ID: req.ID})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			mu.Lock()
			seen[key] = r.Header.Get("Authorization")
			mu.Unlock()
			result := ToolListResult{Tools: []Tool{{Name: toolName, Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
			resultJSON, _ := json.Marshal(result)
			json.NewEncoder(w).Encode(Response{JSONRPC: "2.0", Result: resultJSON, ID: req.ID})
		default:
			http.Error(w, "unexpected method "+req.Method, http.StatusBadRequest)
		}
	}
}

// TestConnectAllTimeout_TwoAuthServers_NoConcurrentMapRace is the blocker
// fix's test: two servers configured with auth (type: bearer, token_env)
// force GetTokenForServer to resolve and SetToken into the shared
// *AuthManager from two goroutines. Before resolving tokens serially (see
// ConnectAllTimeout's doc comment), this raced on AuthManager's
// unsynchronized map -- caught by -race, and capable of a fatal
// "concurrent map writes" throw even without it. Repeating the connect a
// few times in one process raises the odds of catching a race that isn't
// guaranteed to fire on every single run. It also checks each server
// actually received its OWN token, not none or a mixed-up one, since a fix
// that avoided the race by (say) resolving only one server's token would
// pass a lesser version of this test.
func TestConnectAllTimeout_TwoAuthServers_NoConcurrentMapRace(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}

	serverA := httptest.NewServer(mcpAuthHTTPHandler(&mu, seen, "a", "toolA"))
	defer serverA.Close()
	serverB := httptest.NewServer(mcpAuthHTTPHandler(&mu, seen, "b", "toolB"))
	defer serverB.Close()

	t.Setenv("MCP_TEST_TOKEN_A", "token-a-secret")
	t.Setenv("MCP_TEST_TOKEN_B", "token-b-secret")

	writeTestConfig(t, `{"mcpServers":{
		"a":{"url":"`+serverA.URL+`","auth":{"type":"bearer","token_env":"MCP_TEST_TOKEN_A"}},
		"b":{"url":"`+serverB.URL+`","auth":{"type":"bearer","token_env":"MCP_TEST_TOKEN_B"}}
	}}`)

	for i := 0; i < 5; i++ {
		servers, err := ConnectAllTimeout(context.Background(), 3*time.Second)
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		if len(servers) != 2 {
			t.Fatalf("run %d: expected 2 servers connected, got %d: %v", i, len(servers), servers)
		}
		for _, srv := range servers {
			srv.Client.Close()
		}
	}

	mu.Lock()
	gotA, gotB := seen["a"], seen["b"]
	mu.Unlock()
	if gotA != "Bearer token-a-secret" {
		t.Errorf("server a: expected its own Authorization header, got %q", gotA)
	}
	if gotB != "Bearer token-b-secret" {
		t.Errorf("server b: expected its own Authorization header, got %q", gotB)
	}
}
