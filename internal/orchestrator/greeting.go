package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

const (
	maxPlanLines = 10
	maxTitle     = 70
	maxReason    = 120
	maxDetail    = 120
)

// Special is a start-up case that replaces the plan with one short message.
type Special int

// Special cases.
const (
	SpecialNone Special = iota
	NoMilestone
	ReleaseNeeded
	MilestoneEmpty
	NoAccepted
	ForgeError
)

// sanitize drops control characters (ESC, newlines, tabs) and cuts to maxRunes
// runes. It adds no ellipsis.
func sanitize(s string, maxRunes int) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		out = append(out, r)
		if len(out) == maxRunes {
			break
		}
	}
	return string(out)
}

// FormatGreeting builds the start-up text of a plan. Titles are untrusted:
// they are sanitized. Skipped issues print no title.
func FormatGreeting(p PlanReady) string {
	rm := p.Roadmap
	var b strings.Builder
	fmt.Fprintf(&b, "Hello. I work on %s, milestone %s.\n", sanitize(rm.Repo, maxTitle), sanitize(rm.Milestone, maxTitle))
	fmt.Fprintf(&b, "Issues: %d open in %s (%d accepted, %d skipped), %d without milestone.\n",
		rm.Counts.MilestoneOpen, sanitize(rm.Milestone, maxTitle), rm.Counts.MilestoneAccepted, len(rm.Skipped), rm.Counts.NoMilestone)
	if rm.FromOracle {
		b.WriteString("Plan (oracle):\n")
	} else {
		fmt.Fprintf(&b, "Plan (fallback order: %s):\n", sanitize(rm.FallbackWhy, maxReason))
	}
	for i, it := range rm.Items {
		if i == maxPlanLines {
			fmt.Fprintf(&b, "  ... and %d more\n", len(rm.Items)-maxPlanLines)
			break
		}
		fmt.Fprintf(&b, "  %d. #%d %s", i+1, it.Issue, sanitize(it.Title, maxTitle))
		if reason := sanitize(it.Reason, maxReason); reason != "" {
			b.WriteString(" - " + reason)
		}
		b.WriteString("\n")
	}
	if len(rm.Skipped) > 0 {
		parts := make([]string, len(rm.Skipped))
		for i, s := range rm.Skipped {
			parts[i] = fmt.Sprintf("#%d (%s)", s.Issue, sanitize(s.Why, maxReason))
		}
		b.WriteString("Skipped: " + strings.Join(parts, ", ") + "\n")
	}
	if p.Total == 0 {
		b.WriteString("Workers: unlimited.")
	} else {
		fmt.Fprintf(&b, "Workers: %d of %d free.", p.Free, p.Total)
	}
	if len(p.Started) > 0 {
		nums := make([]string, len(p.Started))
		for i, n := range p.Started {
			nums[i] = fmt.Sprintf("#%d", n)
		}
		b.WriteString(" Starting " + strings.Join(nums, ", ") + ".")
	}
	return b.String()
}

// FormatSpecial builds the one-line message of a special case. detail is the
// milestone title, or the error text for ForgeError.
func FormatSpecial(kind Special, detail string) string {
	switch kind {
	case ForgeError:
		first, _, _ := strings.Cut(strings.TrimSpace(detail), "\n")
		return "forge error: " + sanitize(first, maxDetail)
	case NoMilestone:
		return "No open vX.Y.Z milestone. Create one or work on an issue by hand."
	case ReleaseNeeded:
		return fmt.Sprintf("Milestone %s has no open issues left. Release needed (ask me, I do not release yet).", sanitize(detail, maxTitle))
	case MilestoneEmpty:
		return fmt.Sprintf("Milestone %s has no issues. Add issues or work on an issue by hand.", sanitize(detail, maxTitle))
	case NoAccepted:
		return fmt.Sprintf("Milestone %s has open issues, but none has the accepted label from an author with write access.", sanitize(detail, maxTitle))
	}
	return ""
}

// specialFor maps a PlanReady to its special case, with the detail text.
func specialFor(p PlanReady) (Special, string) {
	m := p.Roadmap.Milestone
	switch {
	case errors.Is(p.Special, forge.ErrNoMilestone):
		return NoMilestone, ""
	case errors.Is(p.Special, forge.ErrReleaseNeeded):
		return ReleaseNeeded, m
	case errors.Is(p.Special, forge.ErrMilestoneEmpty):
		return MilestoneEmpty, m
	case p.Special != nil:
		return ForgeError, p.Special.Error()
	case len(p.Roadmap.Items) == 0 && p.Roadmap.Counts.MilestoneOpen > 0:
		return NoAccepted, m
	}
	return SpecialNone, ""
}

// Text returns the chat text of a PlanReady: the greeting, or one special line.
func (p PlanReady) Text() string {
	if k, d := specialFor(p); k != SpecialNone {
		return FormatSpecial(k, d)
	}
	return FormatGreeting(p)
}
