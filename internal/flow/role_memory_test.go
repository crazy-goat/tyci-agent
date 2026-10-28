package flow

import (
	"context"
	"testing"

	"github.com/crazy-goat/tyci-agent/tools"
)

// The role agent gets its repository and role in the context, so the memory
// tool (scope="role") and the prompt builder know whose note they handle.
func TestText_SetsRoleContextForTheAgent(t *testing.T) {
	var gotRepo, gotRole string
	var gotOK bool
	r := newRunner(&spawnRec{})
	r.Spawn = func(ctx context.Context, _ tools.TaskSpec) (string, string, error) {
		gotRepo, gotRole, gotOK = tools.RoleFromContext(ctx)
		return "ok", "sess", nil
	}

	if _, _, err := r.Text(context.Background(), "worker", "", RunContext{Repo: "crazy-goat/tyci-agent", Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !gotOK || gotRepo != "tyci-agent" || gotRole != "worker" {
		t.Fatalf("role context = (%q, %q, %v), want (tyci-agent, worker, true)", gotRepo, gotRole, gotOK)
	}
}

// Without a repository there is no role memory location, so no role context.
func TestText_NoRoleContextWithoutRepo(t *testing.T) {
	var gotOK bool
	r := newRunner(&spawnRec{})
	r.Spawn = func(ctx context.Context, _ tools.TaskSpec) (string, string, error) {
		_, _, gotOK = tools.RoleFromContext(ctx)
		return "ok", "sess", nil
	}

	if _, _, err := r.Text(context.Background(), "worker", "", RunContext{Worktree: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if gotOK {
		t.Fatal("expected no role context when the run has no repository")
	}
}
