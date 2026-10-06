package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

func okCfg() *flowconfig.Config {
	return &flowconfig.Config{
		Models:       map[string]string{"m": "p/m"},
		DefaultModel: "m",
		Roles:        map[string]flowconfig.Role{"worker": {Prompt: "x"}},
	}
}

func okWF() *Workflow {
	return &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Agent: "worker", On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
}

func TestPrepareRun_InvalidWorkflowCreatesNothing(t *testing.T) {
	home := t.TempDir()
	wf := okWF()
	wf.States["a"] = State{Agent: "planner", On: map[string]string{"done": "end"}}
	called := false
	d := PrepareDeps{
		Lookup:   func(string) (*Workflow, string, error) { return wf, "test", nil },
		Config:   func() (*flowconfig.Config, error) { return okCfg(), nil },
		Resolve:  func(rel string) (string, error) { return rel, nil },
		AddIssue: func(context.Context) (*worktree.Worktree, error) { called = true; return nil, nil },
		NewStore: func(id string) (*Store, error) {
			called = true
			return &Store{Dir: filepath.Join(home, ".tyci", "runs", "r", id)}, nil
		},
	}
	if _, _, _, err := PrepareRun(context.Background(), d, PrepareReq{Workflow: "demo", Issue: 1}); err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("AddIssue or NewStore called for invalid workflow")
	}
	for _, p := range []string{".tyci/runs", ".tyci/worktrees"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Fatalf("%s exists", p)
		}
	}
}

func TestPrepareRun_CallOrder(t *testing.T) {
	home := t.TempDir()
	var order []string
	d := PrepareDeps{
		Lookup: func(string) (*Workflow, string, error) {
			order = append(order, "Lookup")
			return okWF(), "test", nil
		},
		Config: func() (*flowconfig.Config, error) { order = append(order, "Config"); return okCfg(), nil },
		Resolve: func(rel string) (string, error) {
			order = append(order, "Validate")
			return rel, nil
		},
		AddIssue: func(context.Context) (*worktree.Worktree, error) {
			order = append(order, "AddIssue")
			return &worktree.Worktree{Dir: "/w", Branch: "b"}, nil
		},
		NewStore: func(id string) (*Store, error) {
			order = append(order, "NewStore")
			return &Store{Dir: filepath.Join(home, id)}, nil
		},
	}
	st, _, _, err := PrepareRun(context.Background(), d, PrepareReq{Workflow: "demo", Repo: "r", Issue: 7})
	if err != nil {
		t.Fatal(err)
	}
	// okWF has no check state, so Validate leaves no trace in Resolve.
	if got := strings.Join(order, ","); got != "Lookup,Config,AddIssue,NewStore" {
		t.Fatalf("order = %s", got)
	}
	if st.Status != "running" || st.Current != "a" || st.Branch != "b" || st.Worktree != "/w" {
		t.Fatalf("bad state: %+v", st)
	}
	if _, err := Load(filepath.Join(home, st.Run)); err != nil {
		t.Fatalf("state not saved: %v", err)
	}
}

func TestPrepareRun_LookupError(t *testing.T) {
	d := PrepareDeps{Lookup: func(string) (*Workflow, string, error) { return nil, "", errors.New("nope") }}
	if _, _, _, err := PrepareRun(context.Background(), d, PrepareReq{}); err == nil {
		t.Fatal("expected error")
	}
}
