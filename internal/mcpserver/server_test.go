package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/mcp"
)

// fakeBackend knows one run. RunsList fails when listErr is set.
type fakeBackend struct{ listErr error }

func (fakeBackend) Status(context.Context) (daemon.Status, error) { return daemon.Status{}, nil }

func (b fakeBackend) RunsList(context.Context) ([]daemon.RunInfo, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return []daemon.RunInfo{{Run: "7", Repo: "acme/demo", Workflow: "fix-ci", Status: "running", Current: "ci"}}, nil
}

func (fakeBackend) RunsGet(_ context.Context, run string) (daemon.RunDetail, error) {
	if run != "7" {
		return daemon.RunDetail{}, errors.New("run not found: " + run)
	}
	st := &flow.RunState{Version: 1, Run: "7", Workflow: "fix-ci", Repo: "acme/demo", Status: "paused", Current: "wait"}
	return daemon.RunDetail{State: st, Path: "/runs/demo/7/state.json"}, nil
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *mcp.RPCError   `json:"error"`
}

// serve sends the lines to a server and returns its replies and its log.
func serve(t *testing.T, b daemon.Backend, lines ...string) ([]reply, string, string) {
	t.Helper()
	var out, log bytes.Buffer
	s := &Server{Backend: b, Version: "test", Log: &log}
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var got []reply
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var r reply
		if err := json.Unmarshal([]byte(l), &r); err != nil || r.JSONRPC != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC 2.0: %q (%v)", l, err)
		}
		got = append(got, r)
	}
	return got, out.String(), log.String()
}

func toolText(t *testing.T, r reply) (string, bool) {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("tools/call error: %+v", r.Error)
	}
	var res mcp.CallToolResult
	if err := json.Unmarshal(r.Result, &res); err != nil || len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("tool result %s (%v)", r.Result, err)
	}
	return res.Content[0].Text, res.IsError
}

func TestMCPServerToolsList(t *testing.T) {
	got, _, _ := serve(t, fakeBackend{},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"two","method":"tools/list"}`,
	)
	if len(got) != 2 {
		t.Fatalf("%d replies, want 2 (no reply to the notification): %+v", len(got), got)
	}
	var init struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		ServerInfo      mcp.ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(got[0].Result, &init); err != nil {
		t.Fatal(err)
	}
	if _, ok := init.Capabilities["tools"]; !ok || init.ProtocolVersion != "2025-06-18" || init.ServerInfo.Name != "tyci" {
		t.Fatalf("initialize result %s", got[0].Result)
	}
	if string(got[1].ID) != `"two"` {
		t.Fatalf("id %s, want the string id back", got[1].ID)
	}
	var list mcp.ToolListResult
	if err := json.Unmarshal(got[1].Result, &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil || schema.Type != "object" {
			t.Fatalf("tool %s input schema %s", tool.Name, tool.InputSchema)
		}
	}
	// Only the read-only tools: a new tool must be added here on purpose.
	if strings.Join(names, ",") != "tyci_runs_list,tyci_run_status" {
		t.Fatalf("tools %v", names)
	}
}

func TestMCPRunStatusJSON(t *testing.T) {
	got, _, _ := serve(t, fakeBackend{},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tyci_run_status","arguments":{"run":"7"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tyci_runs_list","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tyci_run_status","arguments":{"run":"8"}}}`,
	)
	if len(got) != 3 {
		t.Fatalf("%d replies, want 3", len(got))
	}
	text, isErr := toolText(t, got[0])
	var d daemon.RunDetail
	if err := json.Unmarshal([]byte(text), &d); err != nil || isErr || d.State == nil || d.State.Run != "7" || d.State.Status != "paused" || d.Path != "/runs/demo/7/state.json" {
		t.Fatalf("run status text %q (isError %v, %v)", text, isErr, err)
	}
	text, isErr = toolText(t, got[1])
	var runs []daemon.RunInfo
	if err := json.Unmarshal([]byte(text), &runs); err != nil || isErr || len(runs) != 1 || runs[0].Run != "7" {
		t.Fatalf("runs list text %q (isError %v, %v)", text, isErr, err)
	}
	if text, isErr := toolText(t, got[2]); !isErr || !strings.Contains(text, "run not found: 8") {
		t.Fatalf("unknown run: %q (isError %v), want a tool error", text, isErr)
	}
}

func TestMCPUnknownToolError(t *testing.T) {
	got, _, _ := serve(t, fakeBackend{},
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tyci_run_stop","arguments":{"run":"7"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"tyci_run_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
	)
	if len(got) != 4 {
		t.Fatalf("%d replies, want 4", len(got))
	}
	for i, want := range []int{codeInvalidParams, codeInvalidParams, codeNoMethod} {
		if got[i].Error == nil || got[i].Error.Code != want || got[i].Result != nil {
			t.Fatalf("reply %d = %+v, want error code %d", i+1, got[i], want)
		}
	}
	if !strings.Contains(got[0].Error.Message, "unknown tool: tyci_run_stop") {
		t.Fatalf("message %q", got[0].Error.Message)
	}
	if got[3].Error != nil || string(got[3].Result) != "{}" {
		t.Fatalf("ping reply %+v", got[3])
	}
}

func TestMCPNoOutputOnStdoutExceptProtocol(t *testing.T) {
	_, out, log := serve(t, fakeBackend{listErr: errors.New("disk gone")},
		`{not json`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tyci_runs_list"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"nope"}`,
	)
	// serve already checked that every stdout line is JSON-RPC 2.0.
	if n := strings.Count(out, "\n"); n != 3 {
		t.Fatalf("stdout has %d lines, want 3 replies:\n%s", n, out)
	}
	for _, want := range []string{"parse error", "tyci_runs_list: disk gone", "method not found: nope"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log %q has no %q", log, want)
		}
		if strings.Contains(out, "tyci mcp:") {
			t.Fatalf("a log line is on stdout:\n%s", out)
		}
	}
}
