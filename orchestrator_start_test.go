package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/forge/fake"
	"github.com/crazy-goat/tyci-agent/internal/orchestrator"
)

type stubRunner struct {
	mu sync.Mutex
	n  int
}

func (r *stubRunner) Start(context.Context, string, map[string]string) (orchestrator.RunHandle, error) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
	return nil, errors.New("stub")
}

func (r *stubRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

type poster struct{ ch chan string }

func newPoster() *poster { return &poster{ch: make(chan string, 20)} }

func (p *poster) post(s string) { p.ch <- s }

func (p *poster) wait(t *testing.T) string {
	t.Helper()
	select {
	case s := <-p.ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no post")
	}
	return ""
}

// waitNot returns the next post that does not start with the progress prefix.
func (p *poster) waitNot(t *testing.T, prefix string) string {
	t.Helper()
	for {
		if s := p.wait(t); !strings.HasPrefix(s, prefix) {
			return s
		}
	}
}

func TestStartOrchestratorOnce(t *testing.T) {
	f := &fake.Fake{RepoName: "o/r", Ms: []forge.Milestone{{Number: 1, Title: "v0.4.0", OpenCount: 0}}}
	p := newPoster()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	begin := time.Now()
	o := startOrchestrator(ctx, false, orchestrator.Config{Workers: 3}, f, &stubRunner{}, p.post)
	if o == nil || time.Since(begin) > time.Second {
		t.Fatal("did not return at once")
	}
	if got := p.waitNot(t, "Reading milestones"); !strings.Contains(got, "has no issues") {
		t.Fatal(got)
	}
}

func TestResumeDoesNotStart(t *testing.T) {
	r := &stubRunner{}
	p := newPoster()
	if o := startOrchestrator(context.Background(), true, orchestrator.Config{}, &fake.Fake{}, r, p.post); o != nil {
		t.Fatal("started on resume")
	}
	time.Sleep(50 * time.Millisecond)
	if len(p.ch) != 0 || r.starts() != 0 {
		t.Fatal("something ran on resume")
	}
}

func loadForge(t *testing.T, json string) (*flowconfig.Config, error) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tyci", "config.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
	return flowconfig.Load(home, t.TempDir(), false)
}

func TestForgeConfigUnknownKind(t *testing.T) {
	_, err := loadForge(t, `{"forge":{"kind":"gitlab"}}`)
	if err == nil || !strings.Contains(err.Error(), "forge.kind") {
		t.Fatal(err)
	}
	if _, err := newForge(flowconfig.Forge{Kind: "gitlab"}); err == nil {
		t.Fatal("newForge accepted gitlab")
	}
}

func TestForgeConfigRepoAuto(t *testing.T) {
	for _, j := range []string{`{}`, `{"forge":{"repo":"auto"}}`, `{"forge":{"kind":"github","repo":"o/r"}}`} {
		if _, err := loadForge(t, j); err != nil {
			t.Fatalf("%s: %v", j, err)
		}
	}
	if _, err := loadForge(t, `{"forge":{"repo":"nonsense"}}`); err == nil {
		t.Fatal("bad repo accepted")
	}
}

func TestMissingGhOneLine(t *testing.T) {
	g, err := forge.NewGitHub("o/r")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // no gh on PATH
	r := &stubRunner{}
	p := newPoster()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startOrchestrator(ctx, false, orchestrator.Config{Workers: 3}, g, r, p.post)
	got := p.waitNot(t, "Reading milestones")
	if !strings.HasPrefix(got, "forge error:") || strings.Contains(got, "\n") {
		t.Fatalf("%q", got)
	}
	time.Sleep(50 * time.Millisecond)
	if len(p.ch) != 0 || r.starts() != 0 {
		t.Fatal("extra output or runs")
	}
}
