package flow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

// #789: a text run (the roadmap oracle) must not run in the main checkout, and
// its role must be read-only, even when the config allows full permission.
func TestText_RunIsReadOnlyOutsideMainCheckout(t *testing.T) {
	home := t.TempDir()
	e2eWrite(t, filepath.Join(home, ".tyci", "config.json"),
		`{"default_model":"p/m","roles":{"oracle":{"permission":"full"}}}`)
	info := RepoInfo{Home: home, Root: gitRepo(t), Repo: "o/r", DefaultBranch: "main"}
	wf := &Workflow{Name: "roadmap", Start: "plan", States: map[string]State{
		"plan": {Agent: "oracle", Task: "roadmap", On: map[string]string{"done": "end"}},
		"end":  {End: true},
	}}
	rec := &spawnRec{result: "{}"}
	if _, err := NewManager(nil, rec.spawn).Text(context.Background(), info, wf, `{"issues":[]}`); err != nil {
		t.Fatal(err)
	}
	if len(rec.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(rec.specs))
	}
	sp := rec.specs[0]
	if sp.Dir == info.Root || sp.Policy == nil || sp.Policy.Dir != sp.Dir {
		t.Fatalf("dir %q policy %+v: want an own dir, not the main checkout %q", sp.Dir, sp.Policy, info.Root)
	}
	if sp.Policy.Permission != tools.PermissionReadOnly {
		t.Errorf("permission = %q, want %q", sp.Policy.Permission, tools.PermissionReadOnly)
	}
	for _, name := range []string{"write", "edit", "bash"} {
		if tools.AllowedFor(*sp.Policy, name) {
			t.Errorf("tool %q is allowed in a text run", name)
		}
	}
	if _, err := os.Stat(sp.Dir); !os.IsNotExist(err) {
		t.Errorf("run dir %q is still there after the run: %v", sp.Dir, err)
	}
}

// #789: a read-only run gets a read-only policy, so write is refused.
func TestSubagentRunner_ReadOnlyRunDeniesWrite(t *testing.T) {
	s := &spawnRec{result: "{}"}
	r := newRunner(s)
	r.Render = TaskTemplates{}
	if _, _, err := r.Text(context.Background(), "oracle", "roadmap", RunContext{Worktree: t.TempDir(), ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	p := s.specs[0].Policy
	if p == nil || p.Permission != tools.PermissionReadOnly || tools.AllowedFor(*p, "write") {
		t.Errorf("policy = %+v, want read-only without write", p)
	}
}
