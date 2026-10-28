//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// shortHome is wfHome in a short temp dir: the daemon socket path must stay
// under the 104 byte limit of macOS.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	wfWrite(t, filepath.Join(home, ".tyci", "providers.json"), "{}")
	return home
}

func askFlow(t *testing.T, home string) {
	t.Helper()
	body := `{"description":"test","name":"ask-flow","params":[{"name":"issue","description":"issue","required":true}],"start":"check","states":{` +
		`"check":{"check":"checks/ok.sh","on":{"default":"wait"}},` +
		`"wait":{"ask":"need an answer","human":true,"on":{"go":"end"}},` +
		`"end":{"end":true}}}`
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "ask-flow", "workflow.json"), body)
	wfWrite(t, filepath.Join(home, ".tyci", "workflows", "ask-flow", "checks", "ok.sh"), wfOKScript)
}

func TestWorkflowResumeContinuesAPausedRun(t *testing.T) {
	home := wfHome(t)
	wfUseRepo(t, wfRunRepo(t, home))
	askFlow(t, home)
	out, _, err := runWorkflowCLI(t, "workflow", "run", "ask-flow", "9", "--json")
	if err != nil {
		t.Fatal(err)
	}
	paused := decodeWorkflowResult(t, out)
	if paused.Status != "paused" {
		t.Fatalf("run %+v", paused)
	}

	_, _, err = runWorkflowCLI(t, "workflow", "resume", paused.RunID, "nope", "--json")
	if err == nil || !strings.Contains(err.Error(), "allowed: go") {
		t.Fatalf("bad answer: %v", err)
	}
	out, _, err = runWorkflowCLI(t, "workflow", "resume", paused.RunID, "go", "--json")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if r := decodeWorkflowResult(t, out); r.Status != "done" || r.RunID != paused.RunID {
		t.Fatalf("resume %+v", r)
	}
	if _, _, err := runWorkflowCLI(t, "workflow", "resume", paused.RunID, "go"); err == nil || !strings.Contains(err.Error(), "not paused") {
		t.Fatalf("resume of a done run: %v", err)
	}
}

// stubBackend answers runs.get with a fixed state.
type stubBackend struct{ st *flow.RunState }

func (b stubBackend) Status(context.Context) (daemon.Status, error)      { return daemon.Status{}, nil }
func (b stubBackend) RunsList(context.Context) ([]daemon.RunInfo, error) { return nil, nil }
func (b stubBackend) RunsGet(_ context.Context, run string) (daemon.RunDetail, error) {
	if run != b.st.Run {
		return daemon.RunDetail{}, errors.New("run not found: " + run)
	}
	return daemon.RunDetail{State: b.st, Path: "/daemon/state.json"}, nil
}

func listenDaemon(t *testing.T, home string) net.Listener {
	t.Helper()
	dir := daemon.DefaultDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", daemon.SocketPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestWorkflowStatusAsksTheDaemon(t *testing.T) {
	home := shortHome(t)
	ln := listenDaemon(t, home)
	defer func() { _ = ln.Close() }()
	st := &flow.RunState{Version: 1, Run: "77", Workflow: "fix-ci", Repo: "acme/demo", Status: "running", Current: "ci"}
	go daemon.Serve(ln, stubBackend{st}, t.Logf)

	out, _, err := runWorkflowCLI(t, "workflow", "status", "77", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if r := decodeWorkflowResult(t, out); r.Status != "running" || r.Workflow != "fix-ci" || r.StateFile != "/daemon/state.json" {
		t.Fatalf("result %+v", r)
	}
	if _, _, err := runWorkflowCLI(t, "workflow", "status", "78", "--json"); err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Fatalf("unknown run: %v", err)
	}
}

func TestWorkflowStatusFallsBackWhenTheDaemonIsGone(t *testing.T) {
	home := shortHome(t)
	ln := listenDaemon(t, home)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close() // a socket file without a daemon
	const id = "20261005-153012-42"
	wfSaveRun(t, home, "demo", id, "done")

	out, _, err := runWorkflowCLI(t, "workflow", "status", id, "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if r := decodeWorkflowResult(t, out); r.Status != "done" || !strings.HasPrefix(r.StateFile, home) {
		t.Fatalf("result %+v", r)
	}
}

func TestDaemonCommandRefusesWhenDisabled(t *testing.T) {
	home := shortHome(t)
	wfWrite(t, filepath.Join(home, ".tyci", "config.json"), `{"daemon":{"enabled":false}}`)
	resetRootFlags()
	err := runDaemon(context.Background(), daemonCmd, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "daemon.enabled is false") {
		t.Fatalf("err %v", err)
	}
}

func TestDaemonCommandWithoutReposRunsCronOnly(t *testing.T) {
	home := shortHome(t)
	wfUseRepo(t, flow.RepoInfo{Home: home, Root: home, Repo: "acme/demo", DefaultBranch: "main"})
	wfWrite(t, filepath.Join(home, ".tyci", "config.json"), `{"daemon":{"repos":["acme/other"]}}`)
	resetRootFlags()
	ctx, cancel := context.WithCancel(context.Background())
	log := &syncWriter{}
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, daemonCmd, log) }()
	sock := daemon.SocketPath(daemon.DefaultDir(home))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s, err := (&daemon.Client{Path: sock}).Status(context.Background())
	if err != nil || len(s.Repos) != 0 {
		t.Fatalf("status %+v %v", s, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	text := log.String()
	if !strings.Contains(text, "repo acme/other skipped") || !strings.Contains(text, "cron only") {
		t.Fatalf("log:\n%s", text)
	}
}

// syncWriter is a bytes.Buffer that is safe for concurrent use.
type syncWriter struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func TestCronLogDropsNoJobsDue(t *testing.T) {
	var got []string
	l := cronLog(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	_, _ = l.Write([]byte("no jobs due\njob nightly: done\n\n"))
	if len(got) != 1 || got[0] != "cron: job nightly: done" {
		t.Fatalf("log %q", got)
	}
}
