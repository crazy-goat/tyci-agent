package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/agentdefs"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
)

func yes(string) (bool, error) { return true, nil }

func TestBuildInput_FiltersUnacceptedAndNonMembers(t *testing.T) {
	calls := 0
	old := mentionsFn
	mentionsFn = func(b string, s int, k map[int]bool) []int { calls++; return old(b, s, k) }
	defer func() { mentionsFn = old }()
	issues := []forge.Issue{
		{Number: 1, Title: "a", Labels: []string{"accepted"}, Author: "w"},
		{Number: 2, Title: "b", Author: "w"},
		{Number: 3, Title: "c", Labels: []string{"accepted"}, Author: "x"},
	}
	in, sk := buildInput("r/r", "v1", "accepted", issues, func(u string) (bool, error) { return u == "w", nil })
	if len(in.Issues) != 1 || in.Issues[0].Number != 1 || calls != 1 {
		t.Fatalf("in=%+v calls=%d", in, calls)
	}
	want := []Skipped{{2, "no accepted label"}, {3, "author has no write access"}}
	if !reflect.DeepEqual(sk, want) {
		t.Fatalf("skipped=%v", sk)
	}
}

func TestBuildInput_PermissionError(t *testing.T) {
	issues := []forge.Issue{
		{Number: 1, Labels: []string{"accepted"}, Author: "bad"},
		{Number: 2, Labels: []string{"accepted"}, Author: "ok"},
	}
	in, sk := buildInput("r", "m", "accepted", issues, func(u string) (bool, error) {
		if u == "bad" {
			return false, os.ErrInvalid
		}
		return true, nil
	})
	if len(in.Issues) != 1 || len(sk) != 1 || sk[0].Why != "permission check failed" {
		t.Fatalf("in=%+v sk=%v", in, sk)
	}
}

func TestBuildInput_NoBodyInInput(t *testing.T) {
	in, _ := buildInput("r", "m", "accepted", []forge.Issue{{Number: 1, Labels: []string{"accepted"}, Body: "SECRET-BODY"}}, yes)
	b, _ := json.Marshal(in)
	if strings.Contains(string(b), "SECRET-BODY") {
		t.Fatal(string(b))
	}
}

func TestMentions(t *testing.T) {
	known := map[int]bool{12: true, 3: true, 7: true, 9: true, 5: true}
	got := mentions("Depends on #12, blocked by #3, after #7. see #9. after #5 after #99", 5, known)
	if !reflect.DeepEqual(got, []int{3, 7, 12}) {
		t.Fatalf("got %v", got)
	}
}

func in3() Input {
	return Input{Issues: []InputIssue{
		{Number: 1, Title: "one"}, {Number: 2, Title: "two"}, {Number: 3, Title: "three"},
	}}
}

func TestParseRoadmap_Valid(t *testing.T) {
	items, err := parseRoadmap(`{"order":[{"issue":2,"depends_on":[],"reason":"r"},{"issue":1,"depends_on":[2],"reason":"s"},{"issue":3,"depends_on":[],"reason":"t"}]}`, in3())
	if err != nil || len(items) != 3 || items[0].Issue != 2 || items[2].Order != 3 || items[1].DependsOn[0] != 2 {
		t.Fatalf("%+v %v", items, err)
	}
}

func TestParseRoadmap_Errors(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown":   `{"order":[{"issue":9}]}`,
		"duplicate": `{"order":[{"issue":1},{"issue":1}]}`,
		"baddep":    `{"order":[{"issue":1,"depends_on":[9]}]}`,
		"cycle":     `{"order":[{"issue":1,"depends_on":[2]},{"issue":2,"depends_on":[1]}]}`,
		"notjson":   `hello`,
		"selfcycle": `{"order":[{"issue":1,"depends_on":[1]}]}`,
		"emptystr":  ``,
	} {
		if _, err := parseRoadmap(raw, in3()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestParseRoadmap_FencedJSON(t *testing.T) {
	raw := "```json\n{\"order\":[{\"issue\":1,\"depends_on\":[],\"reason\":\"r\"}]}\n```"
	items, err := parseRoadmap(raw, Input{Issues: []InputIssue{{Number: 1}}})
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
}

func TestParseRoadmap_ForgottenIssueAppended(t *testing.T) {
	items, err := parseRoadmap(`{"order":[{"issue":3},{"issue":1}]}`, in3())
	if err != nil || len(items) != 3 {
		t.Fatalf("%+v %v", items, err)
	}
	if last := items[2]; last.Issue != 2 || last.Reason != "not ordered by oracle" || last.Order != 3 {
		t.Fatalf("%+v", last)
	}
}

func nums(items []Item) []int {
	var o []int
	for _, i := range items {
		o = append(o, i.Issue)
	}
	return o
}

func TestFallbackOrder_DependenciesFirst(t *testing.T) {
	in := Input{Issues: []InputIssue{
		{Number: 1, Labels: []string{"type:bug"}, Mentions: []int{2}},
		{Number: 2, Labels: []string{"type:docs"}},
	}}
	if got := nums(fallbackOrder(in)); !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatal(got)
	}
}

func TestFallbackOrder_ScoreThenNumber(t *testing.T) {
	in := Input{Issues: []InputIssue{
		{Number: 1, Labels: []string{"type:docs"}}, {Number: 2, Labels: []string{"type:feature"}},
		{Number: 3, Labels: []string{"type:bug"}}, {Number: 4, Labels: []string{"type:tests"}},
		{Number: 5},
	}}
	if got := nums(fallbackOrder(in)); !reflect.DeepEqual(got, []int{3, 2, 1, 4, 5}) {
		t.Fatal(got)
	}
}

func TestFallbackOrder_TwoTypeLabelsFirstMatchWins(t *testing.T) {
	in := Input{Issues: []InputIssue{
		{Number: 1, Labels: []string{"type:feature"}},
		{Number: 2, Labels: []string{"type:docs", "type:bug"}},
	}}
	if got := nums(fallbackOrder(in)); !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatal(got)
	}
}

func TestFallbackOrder_CycleDoesNotHang(t *testing.T) {
	in := Input{Issues: []InputIssue{
		{Number: 1, Mentions: []int{2}}, {Number: 2, Mentions: []int{1}, Labels: []string{"type:bug"}}, {Number: 3},
	}}
	if got := nums(fallbackOrder(in)); !reflect.DeepEqual(got, []int{3, 2, 1}) {
		t.Fatal(got)
	}
}

func oracleCfg() *flowconfig.Config {
	return &flowconfig.Config{Models: map[string]string{"opus": "x://opus", "cheap": "x://cheap"}}
}

func TestRoadmapWorkflowLoads(t *testing.T) {
	wf, src, err := flow.Lookup("roadmap", t.TempDir(), "", false)
	if err != nil || src != "builtin" {
		t.Fatalf("%v %q", err, src)
	}
	if _, err := flow.Validate(wf, oracleCfg(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestRoadmapWorkflowOverride(t *testing.T) {
	proj := t.TempDir()
	dir := filepath.Join(proj, ".tyci", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"name":"roadmap","start":"end","states":{"end":{"end":true}}}`
	if err := os.WriteFile(filepath.Join(dir, "roadmap.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, src, err := flow.Lookup("roadmap", t.TempDir(), proj, true)
	if err != nil || src == "builtin" {
		t.Fatalf("%v %q", err, src)
	}
}

func TestRoadmapTaskRendersInput(t *testing.T) {
	out, err := flow.RenderTask("roadmap", flow.TaskData{Input: `{"repo":"r"}`})
	if err != nil || !strings.Contains(out, `{"repo":"r"}`) {
		t.Fatalf("%q %v", out, err)
	}
}

func TestOracleHasOnlyReadTool(t *testing.T) {
	defs, err := agentdefs.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defs {
		if d.Name == "oracle" {
			if !reflect.DeepEqual(d.Tools, []string{"read"}) {
				t.Fatalf("tools = %v", d.Tools)
			}
			return
		}
	}
	t.Fatal("no oracle definition")
}

func TestOracleDefaultModel(t *testing.T) {
	cfg := oracleCfg()
	r, err := cfg.Role("oracle")
	if err != nil || r.Model != "opus" || r.Prompt == "" {
		t.Fatalf("%+v %v", r, err)
	}
	cfg.Roles = map[string]flowconfig.Role{"oracle": {Model: "cheap"}}
	r, err = cfg.Role("oracle")
	if err != nil || r.Model != "cheap" || r.Prompt == "" {
		t.Fatalf("%+v %v", r, err)
	}
}
