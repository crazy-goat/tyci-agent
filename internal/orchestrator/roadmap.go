// Package orchestrator decides the order of work on the issues of a milestone.
package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// ItemStatus is the state of one roadmap item.
type ItemStatus string

// Item statuses.
const (
	StatusTodo    ItemStatus = "todo"
	StatusWip     ItemStatus = "wip"
	StatusDone    ItemStatus = "done"
	StatusFailed  ItemStatus = "failed"
	StatusBlocked ItemStatus = "blocked"
	StatusAsk     ItemStatus = "ask"
)

// Item is one issue in the roadmap.
type Item struct {
	Issue     int
	Title     string
	Order     int // 1-based
	DependsOn []int
	Reason    string
	Status    ItemStatus
	RunID     string
}

// Skipped is an issue left out of the plan, with the reason.
type Skipped struct {
	Issue int
	Why   string
}

// Counts are the issue counts shown with the roadmap.
type Counts struct{ MilestoneOpen, MilestoneAccepted, NoMilestone int }

// Roadmap is the in-memory plan. It is never written to the forge.
type Roadmap struct {
	Repo, Milestone string
	Items           []Item
	Skipped         []Skipped
	Counts          Counts
	FromOracle      bool
	FallbackWhy     string // short reason when FromOracle is false
}

// InputIssue is one issue given to the oracle. It has no body.
type InputIssue struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Labels   []string `json:"labels"`
	Mentions []int    `json:"mentions"`
}

// Input is the JSON given to the oracle.
type Input struct {
	Repo      string       `json:"repo"`
	Milestone string       `json:"milestone"`
	Issues    []InputIssue `json:"issues"`
}

var mentionsFn = mentions // test hook

var mentionRe = regexp.MustCompile(`(?i)(?:depends on|blocked by|after)\s+#(\d+)`)

// mentions returns the sorted unique issue numbers in body that are in known, except self.
func mentions(body string, self int, known map[int]bool) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, m := range mentionRe.FindAllStringSubmatch(body, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil || n == self || !known[n] || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// buildInput makes the oracle input from issues. Issues without acceptedLabel
// or from authors without write access are returned in Skipped. Bodies are
// parsed for mentions only and never put into Input.
func buildInput(repo, milestone, acceptedLabel string, issues []forge.Issue, canWrite func(user string) (bool, error)) (Input, []Skipped) {
	in := Input{Repo: repo, Milestone: milestone, Issues: []InputIssue{}}
	var skipped []Skipped
	var eligible []forge.Issue
	known := map[int]bool{}
	for _, is := range issues {
		if !hasLabel(is.Labels, acceptedLabel) {
			skipped = append(skipped, Skipped{is.Number, "no accepted label"})
			continue
		}
		ok, err := canWrite(is.Author)
		if err != nil {
			skipped = append(skipped, Skipped{is.Number, "permission check failed"})
			continue
		}
		if !ok {
			skipped = append(skipped, Skipped{is.Number, "author has no write access"})
			continue
		}
		eligible = append(eligible, is)
		known[is.Number] = true
	}
	for _, is := range eligible {
		labels := is.Labels
		if labels == nil {
			labels = []string{}
		}
		in.Issues = append(in.Issues, InputIssue{Number: is.Number, Title: is.Title, Labels: labels, Mentions: mentionsFn(is.Body, is.Number, known)})
	}
	return in, skipped
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

type oracleAnswer struct {
	Order []struct {
		Issue     int    `json:"issue"`
		DependsOn []int  `json:"depends_on"`
		Reason    string `json:"reason"`
	} `json:"order"`
}

// stripFence removes a leading and trailing Markdown code fence.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	return s
}

// parseRoadmap parses and validates the oracle answer. Issues the oracle
// forgot are appended in fallback order.
func parseRoadmap(raw string, in Input) ([]Item, error) {
	var ans oracleAnswer
	if err := json.Unmarshal([]byte(stripFence(raw)), &ans); err != nil {
		return nil, fmt.Errorf("oracle answer is not valid JSON: %w", err)
	}
	byNum := map[int]InputIssue{}
	for _, is := range in.Issues {
		byNum[is.Number] = is
	}
	deps := map[int][]int{}
	seen := map[int]bool{}
	var items []Item
	for _, o := range ans.Order {
		is, ok := byNum[o.Issue]
		if !ok {
			return nil, fmt.Errorf("oracle ordered unknown issue #%d", o.Issue)
		}
		if seen[o.Issue] {
			return nil, fmt.Errorf("oracle ordered issue #%d twice", o.Issue)
		}
		seen[o.Issue] = true
		for _, d := range o.DependsOn {
			if _, ok := byNum[d]; !ok {
				return nil, fmt.Errorf("issue #%d depends on unknown issue #%d", o.Issue, d)
			}
		}
		deps[o.Issue] = o.DependsOn
		items = append(items, Item{Issue: o.Issue, Title: is.Title, DependsOn: o.DependsOn, Reason: o.Reason, Status: StatusTodo})
	}
	if hasCycle(deps) {
		return nil, errors.New("oracle dependencies contain a cycle")
	}
	for _, it := range fallbackOrder(in) {
		if !seen[it.Issue] {
			it.DependsOn = nil
			it.Reason = "not ordered by oracle"
			items = append(items, it)
		}
	}
	for i := range items {
		items[i].Order = i + 1
	}
	return items, nil
}

func hasCycle(deps map[int][]int) bool {
	state := map[int]int{} // 1 visiting, 2 done
	var visit func(n int) bool
	visit = func(n int) bool {
		switch state[n] {
		case 1:
			return true
		case 2:
			return false
		}
		state[n] = 1
		for _, d := range deps[n] {
			if visit(d) {
				return true
			}
		}
		state[n] = 2
		return false
	}
	for n := range deps {
		if visit(n) {
			return true
		}
	}
	return false
}

// typeWeights is copied from bin/pick-issue.sh (type labels, first match wins).
var typeWeights = []struct {
	label  string
	weight int
}{
	{"type:bug", 50}, {"type:security", 45}, {"type:feature", 20},
	{"type:performance", 15}, {"type:refactor", 10}, {"type:tests", 8}, {"type:docs", 8},
}

func score(labels []string) int {
	for _, w := range typeWeights {
		if hasLabel(labels, w.label) {
			return w.weight
		}
	}
	return 0
}

// fallbackOrder orders by mentions (dependencies first), then label score,
// then issue number. Mentions inside a cycle are ignored.
func fallbackOrder(in Input) []Item {
	byNum := map[int]InputIssue{}
	pending := map[int]bool{}
	for _, is := range in.Issues {
		byNum[is.Number] = is
		pending[is.Number] = true
	}
	better := func(a, b int) bool {
		if sa, sb := score(byNum[a].Labels), score(byNum[b].Labels); sa != sb {
			return sa > sb
		}
		return a < b
	}
	var items []Item
	for len(pending) > 0 {
		best, bestAny := 0, 0
		for n := range pending {
			if bestAny == 0 || better(n, bestAny) {
				bestAny = n
			}
			ready := true
			for _, d := range byNum[n].Mentions {
				if pending[d] {
					ready = false
					break
				}
			}
			if ready && (best == 0 || better(n, best)) {
				best = n
			}
		}
		if best == 0 {
			best = bestAny // cycle: ignore its mentions
		}
		delete(pending, best)
		is := byNum[best]
		items = append(items, Item{Issue: best, Title: is.Title, Order: len(items) + 1,
			DependsOn: is.Mentions, Reason: "fallback order", Status: StatusTodo})
	}
	return items
}
