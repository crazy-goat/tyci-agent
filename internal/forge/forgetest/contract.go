// Package forgetest holds the shared contract every forge.Forge must meet.
package forgetest

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// Fixture is the data a forge under test must serve.
type Fixture struct {
	Milestones []forge.Milestone
	Issues     []forge.Issue
	Writers    map[string]bool
}

// RunContract runs the contract against forges made by newForge.
func RunContract(t *testing.T, newForge func(t *testing.T, fx Fixture) forge.Forge) {
	fx := Fixture{
		Milestones: []forge.Milestone{{Number: 1, Title: "v0.4.0", OpenCount: 1, ClosedCount: 1}},
		Issues: []forge.Issue{
			{Number: 7, Title: "a", Labels: []string{"accepted"}, Author: "alice", Milestone: "v0.4.0", Body: "Depends on #3", State: "open"},
			{Number: 9, Title: "b", Author: "bob", State: "open"},
			{Number: 10, Title: "c", Author: "bob", Milestone: "v0.4.0", State: "closed"},
		},
		Writers: map[string]bool{"alice": true},
	}
	ctx := context.Background()
	f := newForge(t, fx)

	t.Run("milestones", func(t *testing.T) {
		ms, err := f.Milestones(ctx)
		if err != nil || len(ms) != 1 || ms[0] != fx.Milestones[0] {
			t.Fatalf("got %v, %v", ms, err)
		}
	})
	t.Run("issues in milestone", func(t *testing.T) {
		is, err := f.Issues(ctx, "v0.4.0")
		if err != nil || len(is) != 1 || is[0].Number != 7 || is[0].Author != "alice" ||
			is[0].Milestone != "v0.4.0" || len(is[0].Labels) != 1 || is[0].Labels[0] != "accepted" {
			t.Fatalf("got %+v, %v", is, err)
		}
	})
	t.Run("issues without milestone", func(t *testing.T) {
		is, err := f.Issues(ctx, "")
		if err != nil || len(is) != 1 || is[0].Number != 9 {
			t.Fatalf("got %+v, %v", is, err)
		}
	})
	t.Run("unknown milestone", func(t *testing.T) {
		if _, err := f.Issues(ctx, "v9.9.9"); !errors.Is(err, forge.ErrUnknownMilestone) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("can write", func(t *testing.T) {
		if ok, err := f.CanWrite(ctx, "alice"); err != nil || !ok {
			t.Fatalf("alice: %v, %v", ok, err)
		}
		if ok, err := f.CanWrite(ctx, "nobody"); err != nil || ok {
			t.Fatalf("nobody: %v, %v", ok, err)
		}
	})
}
