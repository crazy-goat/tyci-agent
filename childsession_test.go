package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

func childFiles(t *testing.T) []string {
	t.Helper()
	cwd, _ := os.Getwd()
	p, err := session.DefaultPath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(p), "agents", "*.jsonl"))
	return files
}

func TestAgentRunnerRun_WritesChildSessionForJobOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// A scout (no job id) writes no file.
	ctx := connector.WithModelClient(context.Background(), connectortest.Text("scout reply"))
	if _, err := (&agentRunner{}).run(ctx, "scout task", "", "", tools.SubagentOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(childFiles(t)); n != 0 {
		t.Fatalf("scout wrote %d files", n)
	}
	ctx = connector.WithModelClient(context.Background(), connectortest.Text("job reply"))
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, "job-cs-1")
	if _, err := (&agentRunner{}).run(ctx, "job task", "", "", tools.SubagentOptions{}); err != nil {
		t.Fatal(err)
	}
	files := childFiles(t)
	if len(files) != 1 || !strings.Contains(files[0], "job-cs-1") {
		t.Fatalf("files = %v", files)
	}
	b, _ := os.ReadFile(files[0])
	s := string(b)
	for _, want := range []string{"job task", "job reply"} {
		if !strings.Contains(s, want) {
			t.Fatalf("file lacks %q: %s", want, s)
		}
	}
	if strings.Contains(s, "session_end") {
		t.Fatalf("file has session_end: %s", s)
	}

	// Two forks of one job get separate files and leave the source unchanged.
	resumableMu.Lock()
	entry := resumable["job-cs-1"]
	resumableMu.Unlock()
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "resume X"}}}}
	a := forkChildSession(entry.cfg.Session, "m", "p", "job-cs-2")
	b2 := forkChildSession(entry.cfg.Session, "m", "p", "job-cs-3")
	if a == nil || b2 == nil || a.Path() == b2.Path() {
		t.Fatal("fork failed or shares a path")
	}
	writeChildMessages(a, msgs)
	_ = a.Close()
	_ = b2.Close()
	ab, _ := os.ReadFile(a.Path())
	bb, _ := os.ReadFile(b2.Path())
	src, _ := os.ReadFile(files[0])
	if !strings.Contains(string(ab), "resume X") || strings.Contains(string(bb), "resume X") || strings.Contains(string(src), "resume X") {
		t.Fatal("fork branches are not separate")
	}
	if !strings.Contains(string(ab), "job reply") {
		t.Fatal("fork lacks source history")
	}
}

func TestTakeTranscriptPath_ClearsKeyForGrandchildren(t *testing.T) {
	ctx := context.WithValue(context.Background(), tools.TranscriptCtxKey{}, "/x/t.jsonl")
	tp, inner := takeTranscriptPath(ctx)
	if tp != "/x/t.jsonl" {
		t.Fatalf("tp = %q", tp)
	}
	if again, _ := takeTranscriptPath(inner); again != "" {
		t.Fatalf("grandchild sees %q", again)
	}
}

func TestAgentRunnerRun_RunTranscriptReplacesChildSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tp := filepath.Join(t.TempDir(), "agents", "001-worker.jsonl")
	ctx := connector.WithModelClient(context.Background(), connectortest.Text("reply ghp_abcdefghijklmnopqrstuvwxyz0123456789"))
	ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, "job-rt-1")
	ctx = context.WithValue(ctx, tools.TranscriptCtxKey{}, tp)
	if _, err := (&agentRunner{}).run(ctx, "task", "", "", tools.SubagentOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(childFiles(t)); n != 0 {
		t.Fatalf("sessions dir has %d child files", n)
	}
	b, err := os.ReadFile(tp)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ghp_abcdefghij") || !strings.Contains(string(b), "reply") {
		t.Fatalf("transcript not redacted or empty: %s", b)
	}
}
