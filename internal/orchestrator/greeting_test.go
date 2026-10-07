package orchestrator

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

func plan(n int) PlanReady {
	rm := Roadmap{Repo: "o/r", Milestone: "v0.4.0", FromOracle: true, Counts: Counts{MilestoneOpen: n + 1, MilestoneAccepted: n, NoMilestone: 3}}
	for i := 1; i <= n; i++ {
		rm.Items = append(rm.Items, Item{Issue: i, Title: fmt.Sprintf("title %d", i), Order: i, Reason: "why"})
	}
	rm.Skipped = []Skipped{{Issue: 9, Why: "no accepted label"}}
	return PlanReady{Roadmap: rm, Free: 1, Total: 3, Started: []int{1, 2}}
}

func TestFormatGreeting_Plan(t *testing.T) {
	want := "Hello. I work on o/r, milestone v0.4.0.\n" +
		"Issues: 4 open in v0.4.0 (3 accepted, 1 skipped), 3 without milestone.\n" +
		"Plan (oracle):\n  1. #1 title 1 - why\n  2. #2 title 2 - why\n  3. #3 title 3 - why\n" +
		"Skipped: #9 (no accepted label)\n" +
		"Workers: 1 of 3 free. Starting #1, #2."
	if got := FormatGreeting(plan(3)); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatGreeting_Fallback(t *testing.T) {
	p := plan(1)
	p.Roadmap.FromOracle, p.Roadmap.FallbackWhy = false, "oracle timed out"
	got := FormatGreeting(p)
	if !strings.Contains(got, "Plan (fallback order: oracle timed out):") {
		t.Fatal(got)
	}
}

func TestFormatGreeting_LongPlanTruncated(t *testing.T) {
	got := FormatGreeting(plan(15))
	if strings.Count(got, "\n  ")+0 != 11 || !strings.Contains(got, "  ... and 5 more\n") || strings.Contains(got, "#11 ") {
		t.Fatal(got)
	}
}

func TestFormatGreeting_SanitizesTitle(t *testing.T) {
	p := plan(2)
	p.Roadmap.Items[0].Title = "a\x1b[31mb\nc"
	p.Roadmap.Items[1].Title = strings.Repeat("é", 71)
	got := FormatGreeting(p)
	if strings.ContainsRune(got, '\x1b') || !strings.Contains(got, "#1 a[31mbc - why") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "#2 "+strings.Repeat("é", 70)+" - why") {
		t.Fatal(got)
	}
	if s := sanitize(strings.Repeat("a", 75), 70); s != strings.Repeat("a", 70) {
		t.Fatal(s)
	}
}

func TestFormatGreeting_SkippedHasNoTitle(t *testing.T) {
	p := plan(1)
	p.Roadmap.Skipped = []Skipped{{Issue: 9, Why: "no accepted label"}, {Issue: 10, Why: "author has no write access"}}
	got := FormatGreeting(p)
	if !strings.Contains(got, "Skipped: #9 (no accepted label), #10 (author has no write access)\n") {
		t.Fatal(got)
	}
}

func TestFormatGreeting_UnlimitedWorkers(t *testing.T) {
	p := plan(1)
	p.Total, p.Free = 0, -1
	if got := FormatGreeting(p); !strings.Contains(got, "Workers: unlimited. Starting #1, #2.") {
		t.Fatal(got)
	}
	p.Started = nil
	if got := FormatGreeting(p); !strings.HasSuffix(got, "Workers: unlimited.") {
		t.Fatal(got)
	}
}

func TestFormatSpecial(t *testing.T) {
	long := strings.Repeat("x", 200)
	tests := []struct {
		kind   Special
		detail string
		want   string
	}{
		{ForgeError, "gh down\nsecond", "forge error: gh down"},
		{ForgeError, long, "forge error: " + strings.Repeat("x", 120)},
		{NoMilestone, "", "No open vX.Y.Z milestone. Create one or work on an issue by hand."},
		{ReleaseNeeded, "v0.4.0", "Milestone v0.4.0 has no open issues left. Release needed (ask me, I do not release yet)."},
		{MilestoneEmpty, "v0.4.0", "Milestone v0.4.0 has no issues. Add issues or work on an issue by hand."},
		{NoAccepted, "v0.4.0", "Milestone v0.4.0 has open issues, but none has the accepted label from an author with write access."},
	}
	for _, tc := range tests {
		if got := FormatSpecial(tc.kind, tc.detail); got != tc.want {
			t.Errorf("%d: got %q want %q", tc.kind, got, tc.want)
		}
	}
}

func TestPlanReadyText(t *testing.T) {
	if got := (PlanReady{Special: forge.ErrNoMilestone}).Text(); !strings.HasPrefix(got, "No open") {
		t.Fatal(got)
	}
	if got := (PlanReady{Special: errors.New("boom")}).Text(); got != "forge error: boom" {
		t.Fatal(got)
	}
	p := PlanReady{Roadmap: Roadmap{Milestone: "v1.0.0", Counts: Counts{MilestoneOpen: 2}}}
	if got := p.Text(); !strings.Contains(got, "none has the accepted label") {
		t.Fatal(got)
	}
	if got := plan(1).Text(); !strings.HasPrefix(got, "Hello.") {
		t.Fatal(got)
	}
}
