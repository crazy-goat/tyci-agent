package forge_test

import (
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

func titles(ts ...string) []forge.Milestone {
	var ms []forge.Milestone
	for _, t := range ts {
		ms = append(ms, forge.Milestone{Title: t})
	}
	return ms
}

func TestLowestMilestone_NumericOrder(t *testing.T) {
	m, err := forge.LowestMilestone(titles("v0.10.0", "v0.9.0", "v0.2.0"))
	if err != nil || m.Title != "v0.2.0" {
		t.Fatalf("got %v, %v", m, err)
	}
	m, err = forge.LowestMilestone(titles("v0.10.0", "v0.9.0"))
	if err != nil || m.Title != "v0.9.0" {
		t.Fatalf("got %v, %v", m, err)
	}
}

func TestLowestMilestone_IgnoresNonVersion(t *testing.T) {
	m, err := forge.LowestMilestone(titles("backlog", "v1", "v0.1.0"))
	if err != nil || m.Title != "v0.1.0" {
		t.Fatalf("got %v, %v", m, err)
	}
	if _, err := forge.LowestMilestone(titles("backlog", "v1")); !errors.Is(err, forge.ErrNoMilestone) {
		t.Fatalf("got %v", err)
	}
}

func TestMilestoneState(t *testing.T) {
	if err := forge.MilestoneState(forge.Milestone{ClosedCount: 3}); !errors.Is(err, forge.ErrReleaseNeeded) {
		t.Error(err)
	}
	if err := forge.MilestoneState(forge.Milestone{}); !errors.Is(err, forge.ErrMilestoneEmpty) {
		t.Error(err)
	}
	if err := forge.MilestoneState(forge.Milestone{OpenCount: 2}); err != nil {
		t.Error(err)
	}
}
