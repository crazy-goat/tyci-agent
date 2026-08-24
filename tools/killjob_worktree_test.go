package tools

import (
	"context"
	"os"
	"testing"

	"github.com/decodo/tyci/internal/worktree"
)

// TestFinishWorktreeRunsDespiteCancelledContext: cleanup must happen even
// when the child was cancelled — runSingleTask defers finishWorktree, which
// detaches its ctx via WithoutCancel, so a dead context never leaves a
// directory and branch behind. We exercise finishWorktree directly (a full
// isolation:"worktree" child is heavy) using the real worktree package:
// create a checkout, change nothing, line its ctx up with a cancelled one,
// and finishWorktree must remove the checkout.
//
// Revert check: drop the `cleanupCtx := context.WithoutCancel(ctx)` line in
// finishWorktree (i.e. call wt.Changed with the dead ctx) and the directory
// is NOT removed — this test fails with the checkout still present.
func TestFinishWorktreeRunsDespiteCancelledContext(t *testing.T) {
	repo := gitRepo(t)

	// Create the checkout while the context is alive (worktree.Add runs git);
	// the child is cancelled afterwards, and cleanup must still run.
	wt, err := worktree.Add(context.Background(), repo, "test-cleanup")
	if err != nil {
		t.Fatalf("worktree.Add: %v", err)
	}
	if _, err := os.Stat(wt.Dir); err != nil {
		t.Fatalf("checkout was not created: %v", err)
	}
	// Confirm unchanged (clean status): the created checkout has nothing
	// committed, so Changed should be false — the "remove" branch.
	if changed, err := wt.Changed(context.Background()); err != nil {
		t.Fatalf("revert-check setup: Changed failed: %v", err)
	} else if changed {
		t.Fatal("revert-check setup: expected a clean (unchanged) checkout")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before cleanup runs

	finishWorktree(ctx, wt)

	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Fatalf("checkout still exists after finishWorktree with a cancelled ctx: %v", err)
	}
}
