//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/mcp"
)

// runMCPServe runs 'tyci mcp serve' with the lines on stdin and returns the
// tool texts of the replies (in order) and stderr. It fails when a stdout
// line is not a JSON-RPC 2.0 reply.
func runMCPServe(t *testing.T, lines ...string) ([]string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wfCommandTimeout)
	t.Cleanup(cancel)
	var out, errOut bytes.Buffer
	rootCmd.SetIn(strings.NewReader(strings.Join(lines, "\n") + "\n"))
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"mcp", "serve"})
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		t.Fatalf("mcp serve: %v (stderr %q)", err, errOut.String())
	}
	var texts []string
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		var r struct {
			JSONRPC string             `json:"jsonrpc"`
			Result  mcp.CallToolResult `json:"result"`
			Error   *mcp.RPCError      `json:"error"`
		}
		if err := json.Unmarshal([]byte(l), &r); err != nil || r.JSONRPC != "2.0" || r.Error != nil || len(r.Result.Content) != 1 {
			t.Fatalf("stdout line %q (%v)", l, err)
		}
		texts = append(texts, r.Result.Content[0].Text)
	}
	return texts, errOut.String()
}

func TestMCPServeFallsBackToRunFilesWithoutDaemon(t *testing.T) {
	home := shortHome(t)
	ln := listenDaemon(t, home)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close() // a socket file without a daemon
	const open, done = "20261005-153012-42", "20261005-153013-43"
	wfSaveRun(t, home, "demo", open, "running")
	wfSaveRun(t, home, "demo", done, "done")
	old := workflowRepoInfo
	workflowRepoInfo = func(string) (flow.RepoInfo, error) { return flow.RepoInfo{Home: home, Repo: "acme/demo"}, nil }
	t.Cleanup(func() { workflowRepoInfo = old })

	texts, _ := runMCPServe(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tyci_runs_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tyci_run_status","arguments":{"run":"`+done+`"}}}`,
	)
	var runs []daemon.RunInfo
	if err := json.Unmarshal([]byte(texts[0]), &runs); err != nil || len(runs) != 1 || runs[0].Run != open || runs[0].Repo != "acme/demo" {
		t.Fatalf("tyci_runs_list = %s (%v), want the open run of the run files", texts[0], err)
	}
	var d daemon.RunDetail
	if err := json.Unmarshal([]byte(texts[1]), &d); err != nil || d.State == nil || d.State.Status != "done" || !strings.HasPrefix(d.Path, home) {
		t.Fatalf("tyci_run_status = %s (%v), want the run file", texts[1], err)
	}
}

func TestMCPServeOutsideARepositoryLogsToStderr(t *testing.T) {
	shortHome(t)
	old := workflowRepoInfo
	workflowRepoInfo = func(string) (flow.RepoInfo, error) { return flow.RepoInfo{}, errors.New("not in a git repository") }
	t.Cleanup(func() { workflowRepoInfo = old })

	texts, errOut := runMCPServe(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tyci_runs_list","arguments":{}}}`)
	if strings.TrimSpace(texts[0]) != "[]" || !strings.Contains(errOut, "not in a git repository") {
		t.Fatalf("tyci_runs_list = %q, stderr %q", texts[0], errOut)
	}
}
