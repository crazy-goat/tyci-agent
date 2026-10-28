package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// #784: a copied milestone-close workflow with the pick state and its task renamed still saves
// the pick answer, so the apply check finds it and returns done.
func TestSaves_RenamedPickStateAppliesFindings(t *testing.T) {
	home := t.TempDir()
	wf := initWF(t, home, "milestone-close", "milestone-close")
	renameInWorkflow(t, wf.Source, map[string]string{`"pick"`: `"choose"`, "pick_critical": "choose_findings"})
	if err := os.Rename(filepath.Join(wf.Source, "tasks", "pick_critical.md"), filepath.Join(wf.Source, "tasks", "choose_findings.md")); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("milestone-close", home, "", false)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	testutil.StubGH(t, `exit 0`)

	runDir := t.TempDir()
	cands := `[{"number":1,"title":"a","body":""}]`
	if err := os.WriteFile(filepath.Join(runDir, "candidates.json"), []byte(cands), 0o600); err != nil {
		t.Fatal(err)
	}
	spawn := &spawnRec{result: `{"critical":[1],"doubt":[]}`}
	checks := &ExecChecker{Resolve: func(rel string) (string, error) { return filepath.Join(wf.Source, rel), nil }}
	r := &Runner{WF: wf, RunDir: runDir, Checks: checks, Agents: newRunner(spawn)}
	st := newRun("choose")
	st.Params = map[string]string{"milestone": "v1"}
	st.Worktree = t.TempDir()
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(runDir, "artifacts", "001-choose", "output.txt")); err != nil {
		t.Fatalf("pick answer not saved: %v", err)
	}
	if st.Result != "done" {
		t.Fatalf("Result = %q, history %+v", st.Result, st.History)
	}
}

// renameInWorkflow replaces each old text with its new text in the workflow.json of dir.
func renameInWorkflow(t *testing.T, dir string, repl map[string]string) {
	t.Helper()
	path := filepath.Join(dir, "workflow.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for from, to := range repl {
		text = strings.ReplaceAll(text, from, to)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// #784: a state with the flag saves. Without any flag, the legacy task names do, on an
// oracle state only. A flag on another state turns the legacy names off.
func TestSavesAnswer_FlagAndLegacyNames(t *testing.T) {
	flagged := &Workflow{States: map[string]State{
		"a": {Agent: "oracle", Task: "pick_critical", Saves: true},
		"b": {Agent: "oracle", Task: "pick_critical"},
	}}
	if !savesAnswer(flagged, "a") || savesAnswer(flagged, "b") {
		t.Error("the flag must save its state and not the legacy name of another state")
	}
	legacy := &Workflow{States: map[string]State{
		"pick":   {Agent: "oracle", Task: "pick_critical"},
		"plan":   {Agent: "oracle", Task: "plan"},
		"worker": {Agent: "worker", Task: "plan"},
		"choose": {Agent: "oracle", Task: "choose_findings"},
	}}
	if !savesAnswer(legacy, "pick") || !savesAnswer(legacy, "plan") {
		t.Error("a workflow without the flag must keep the legacy names")
	}
	if savesAnswer(legacy, "worker") || savesAnswer(legacy, "choose") {
		t.Error("a non-oracle state or a renamed task must not save without the flag")
	}
}

// #784: the builtin templates set the flag. issue-to-merge has no flag and keeps the plan name.
func TestSavesAnswer_TemplatesSetFlag(t *testing.T) {
	if wf := initWF(t, t.TempDir(), "milestone-close", "milestone-close"); !wf.States["pick"].Saves {
		t.Error("milestone-close pick has no saves flag")
	}
	if wf := initWF(t, t.TempDir(), "rebalance", "rebalance"); !wf.States["split"].Saves {
		t.Error("rebalance split has no saves flag")
	}
	wf := initWF(t, t.TempDir(), "issue-to-merge", "issue-to-merge")
	if !savesAnswer(wf, "plan") {
		t.Error("issue-to-merge plan must keep saving by its legacy name")
	}
}

// #784: savedAnswer names the newest saved oracle answer of the run, or nothing.
func TestSavedAnswer_NewestSavingOracleStep(t *testing.T) {
	wf := &Workflow{States: map[string]State{
		"choose":  {Agent: "oracle", Task: "choose_findings", Saves: true},
		"recover": {Agent: "oracle", Task: "recover"},
	}}
	r := &Runner{WF: wf, RunDir: "/r"}
	st := newRun("")
	if got := r.savedAnswer(st); got != "" {
		t.Fatalf("no history: got %q", got)
	}
	st.History = []Step{
		{Kind: "agent", Role: "oracle", State: "choose", Artifact: "001-choose"},
		{Kind: "check", State: "apply", Artifact: "002-apply"},
		{Kind: "agent", Role: "oracle", State: "recover", Artifact: "003-recover"},
	}
	if got, want := r.savedAnswer(st), "/r/artifacts/001-choose/output.txt"; got != want {
		t.Fatalf("savedAnswer = %q, want %q", got, want)
	}
}
