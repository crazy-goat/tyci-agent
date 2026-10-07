// Package fake is an in-memory forge.Forge for tests.
package fake

import (
	"context"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// Fake is an in-memory forge. When Err is set every method returns it.
type Fake struct {
	Ms       []forge.Milestone
	Is       []forge.Issue
	Writers  map[string]bool
	Err      error
	RepoName string
}

var _ forge.Forge = (*Fake)(nil)

// Repo returns RepoName.
func (f *Fake) Repo() string { return f.RepoName }

// Milestones returns Ms.
func (f *Fake) Milestones(context.Context) ([]forge.Milestone, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Ms, nil
}

// Issues returns the open issues of a milestone title ("" = no milestone).
func (f *Fake) Issues(_ context.Context, milestone string) ([]forge.Issue, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	if milestone != "" {
		found := false
		for _, m := range f.Ms {
			found = found || m.Title == milestone
		}
		if !found {
			return nil, forge.ErrUnknownMilestone
		}
	}
	out := []forge.Issue{}
	for _, i := range f.Is {
		if i.State == "open" && i.Milestone == milestone {
			out = append(out, i)
		}
	}
	return out, nil
}

// CanWrite reports Writers[user].
func (f *Fake) CanWrite(_ context.Context, user string) (bool, error) {
	if f.Err != nil {
		return false, f.Err
	}
	return f.Writers[user], nil
}
