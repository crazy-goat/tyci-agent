// Package forge is a minimal read-only view of a code forge: milestones,
// issues, labels and write permission.
//
// The milestone rule (work on the lowest open vX.Y.Z milestone; no open issues
// left means a release is needed; no issues at all means the milestone is
// empty) comes from bin/pick-issue.sh. This package re-implements it in Go and
// does not call the script.
package forge

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

// Milestone is a forge milestone.
type Milestone struct {
	// Number is the forge id of the milestone.
	Number int
	// Title is the milestone name, for example "v0.4.0".
	Title string
	// OpenCount is the number of open issues in the milestone.
	OpenCount int
	// ClosedCount is the number of closed issues in the milestone.
	ClosedCount int
}

// Issue is a forge issue.
type Issue struct {
	// Number is the issue number.
	Number int
	// Title is the issue title.
	Title string
	// Labels are the label names.
	Labels []string
	// Author is the login of the issue author.
	Author string
	// Milestone is the milestone title, or "" when there is none.
	Milestone string
	// Body is never passed to agents; it is only for dependency parsing.
	Body string
	// State is "open" or "closed".
	State string
}

var (
	// ErrReleaseNeeded means the milestone has no open issues left.
	ErrReleaseNeeded = errors.New("forge: milestone has no open issues left")
	// ErrMilestoneEmpty means the milestone has no issues at all.
	ErrMilestoneEmpty = errors.New("forge: milestone has no issues at all")
	// ErrNoMilestone means there is no open vX.Y.Z milestone.
	ErrNoMilestone = errors.New("forge: no open vX.Y.Z milestone")
	// ErrUnknownMilestone means the named milestone does not exist.
	ErrUnknownMilestone = errors.New("forge: milestone not found")
)

// Forge reads a code forge for one repository.
type Forge interface {
	// Repo returns the repository as "owner/name".
	Repo() string
	// Milestones returns the open milestones.
	Milestones(ctx context.Context) ([]Milestone, error)
	// Issues returns the open issues of a milestone title; "" means issues
	// without a milestone. An unknown title returns ErrUnknownMilestone.
	Issues(ctx context.Context, milestone string) ([]Issue, error)
	// CanWrite reports whether user has write or admin permission.
	CanWrite(ctx context.Context, user string) (bool, error)
}

var versionRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

func version(title string) ([3]int, bool) {
	m := versionRe.FindStringSubmatch(title)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// LowestMilestone returns the milestone with the lowest vX.Y.Z title. Other
// titles are ignored. It returns ErrNoMilestone when none match.
func LowestMilestone(ms []Milestone) (Milestone, error) {
	var best Milestone
	var bestV [3]int
	found := false
	for _, m := range ms {
		v, ok := version(m.Title)
		if !ok {
			continue
		}
		if !found || less(v, bestV) {
			best, bestV, found = m, v, true
		}
	}
	if !found {
		return Milestone{}, ErrNoMilestone
	}
	return best, nil
}

func less(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// MilestoneState returns ErrReleaseNeeded when all issues are closed,
// ErrMilestoneEmpty when there are no issues, and nil otherwise.
func MilestoneState(m Milestone) error {
	switch {
	case m.OpenCount == 0 && m.ClosedCount > 0:
		return ErrReleaseNeeded
	case m.OpenCount == 0:
		return ErrMilestoneEmpty
	}
	return nil
}
