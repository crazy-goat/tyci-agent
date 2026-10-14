package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// oracleAgents runs the oracle role with scripted answers. Other roles go to inner.
type oracleAgents struct {
	inner   *fakeAgents
	answers []string
	err     error
	calls   []RunContext
	tasks   []string
	runIDs  []string // the run ID on the context of each oracle call
}

func (o *oracleAgents) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	if role != "oracle" {
		return o.inner.Run(ctx, role, task, rc)
	}
	o.calls = append(o.calls, rc)
	o.tasks = append(o.tasks, task)
	runID, _ := ctx.Value(jobs.RunIDCtxKey{}).(string)
	o.runIDs = append(o.runIDs, runID)
	if o.err != nil {
		return "", "", o.err
	}
	if len(o.calls) > len(o.answers) {
		return "", "", errors.New("oracleAgents: no scripted answer")
	}
	return o.answers[len(o.calls)-1], "sess-oracle", nil
}

// askWF loops code to a max_visits pause (limit 1). The ask state is human when human is set.
func askWF(human bool) *Workflow {
	return &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code": {Agent: "coder", MaxVisits: 1, On: map[string]string{"done": "code"}},
		"ask":  {Ask: "The run needs a human decision.", Human: human, On: map[string]string{"retry": "code", "stop": "end"}},
		"end":  {End: true},
	}}
}

type oracleFixture struct {
	r       *Runner
	st      *RunState
	oa      *oracleAgents
	notices []string
}

func newOracleFixture(wf *Workflow, answers ...string) *oracleFixture {
	f := &oracleFixture{oa: &oracleAgents{
		answers: answers,
		inner:   &fakeAgents{keys: map[string][]string{"code": manyKeys("done", 20)}},
	}}
	f.r = &Runner{WF: wf, Store: &memStore{}, Agents: f.oa, Warn: func(msg string) {
		f.notices = append(f.notices, msg)
	}}
	f.st = newRun("")
	return f
}

func oracleSteps(st *RunState) []Step {
	var out []Step
	for _, h := range st.History {
		if h.Kind == "ask" && h.Role == "oracle" {
			out = append(out, h)
		}
	}
	return out
}

func TestOracle_RetryNoteReachesWorkerThenStopEnds(t *testing.T) {
	f := newOracleFixture(askWF(false), "retry use the new flag\nthe worker missed the flag", "stop\nno more work")
	if err := f.r.Run(context.Background(), f.st); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if f.st.Status != "done" {
		t.Fatalf("status %q, want done", f.st.Status)
	}
	if len(f.oa.calls) != 2 || f.oa.tasks[0] != "ask" {
		t.Fatalf("oracle calls %d tasks %v", len(f.oa.calls), f.oa.tasks)
	}
	first := f.oa.calls[0]
	if first.Reason != "max_visits:code" || first.Goto != "code" || !strings.Contains(first.Pause, "The run needs a human decision.") {
		t.Fatalf("oracle context reason %q goto %q pause %q", first.Reason, first.Goto, first.Pause)
	}
	if len(f.oa.inner.rcs) < 2 || f.oa.inner.rcs[1].Note != "use the new flag" {
		t.Fatalf("worker did not get the note: %+v", f.oa.inner.rcs)
	}
	steps := oracleSteps(f.st)
	if len(steps) != 2 {
		t.Fatalf("oracle steps %d, want 2", len(steps))
	}
	if steps[0].Key != "retry use the new flag" || steps[0].To != "code" || steps[0].Note != "the worker missed the flag" {
		t.Fatalf("first oracle step %+v", steps[0])
	}
	want := "workflow run test-run: oracle answered retry use the new flag: the worker missed the flag"
	if len(f.notices) == 0 || f.notices[0] != want {
		t.Fatalf("notices %q, want first %q", f.notices, want)
	}
}

func TestOracle_JobCarriesRunID(t *testing.T) {
	f := newOracleFixture(askWF(false), "retry use the new flag\nthe worker missed the flag", "stop\nno more work")
	if err := f.r.Run(context.Background(), f.st); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(f.oa.runIDs) != 2 {
		t.Fatalf("oracle calls %d, want 2", len(f.oa.runIDs))
	}
	for i, id := range f.oa.runIDs {
		if id != "test-run" {
			t.Fatalf("oracle call %d run ID %q, want test-run", i, id)
		}
	}
}

func TestOracle_HumanStateIsNotAsked(t *testing.T) {
	f := newOracleFixture(askWF(true), "retry\nreason")
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if len(f.oa.calls) != 0 || f.st.Status != "paused" || f.st.Current != "ask" {
		t.Fatalf("oracle calls %d status %q current %q", len(f.oa.calls), f.st.Status, f.st.Current)
	}
}

func TestOracle_AnswerLimit(t *testing.T) {
	two, one, zero := 2, 1, 0
	cases := []struct {
		name  string
		limit *int
		calls int
	}{
		{"default is two", nil, 2},
		{"explicit two", &two, 2},
		{"one", &one, 1},
		{"zero turns the oracle off", &zero, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wf := askWF(false)
			wf.Defaults.OracleAnswers = c.limit
			f := newOracleFixture(wf, "retry\nagain", "retry\nagain", "retry\nagain")
			if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
				t.Fatalf("err = %v, want ErrPaused", err)
			}
			if len(f.oa.calls) != c.calls || len(oracleSteps(f.st)) != c.calls {
				t.Fatalf("oracle calls %d steps %d, want %d", len(f.oa.calls), len(oracleSteps(f.st)), c.calls)
			}
			if f.st.Status != "paused" {
				t.Fatalf("status %q, want paused", f.st.Status)
			}
		})
	}
}

func TestOracle_ProtectedMergeGoesToHuman(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "merge", States: map[string]State{
		"merge": {Check: "merge.sh", On: map[string]string{"protected": "ask", "merged": "end"}},
		"ask":   {Ask: "The run needs a human decision.", On: map[string]string{"retry": "merge", "stop": "end"}},
		"end":   {End: true},
	}}
	f := newOracleFixture(wf, "retry\nreason")
	f.r.Checks = &fakeChecks{keys: map[string][]string{"merge.sh": {"protected"}}}
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if len(f.oa.calls) != 0 {
		t.Fatalf("oracle called %d times for a protected merge", len(f.oa.calls))
	}
	if !strings.Contains(f.st.Ask.Message, "(last step: merge, key protected)") {
		t.Fatalf("message %q", f.st.Ask.Message)
	}
}

func TestOracle_GotoMergeNeedsHuman(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":  {Agent: "coder", MaxVisits: 1, On: map[string]string{"done": "code"}},
		"merge": {Check: "merge.sh", On: map[string]string{"merged": "end", "default": "end"}},
		"ask":   {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
		"end":   {End: true},
	}}
	f := newOracleFixture(wf, "goto merge\nthe CI timed out and the change looks ready")
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if f.st.Status != "paused" || f.st.Current != "ask" {
		t.Fatalf("status %q current %q, want paused at ask", f.st.Status, f.st.Current)
	}
	if !strings.Contains(f.st.Ask.Message, "The oracle proposes a merge, a human must confirm: the CI timed out and the change looks ready") {
		t.Fatalf("message %q", f.st.Ask.Message)
	}
	steps := oracleSteps(f.st)
	if len(steps) != 1 || steps[0].Key != "goto:merge" {
		t.Fatalf("oracle steps %+v", steps)
	}
}

// The real subagent runner returns the trimmed text of the ask task as the answer.
func TestOracle_SubagentRunnerReturnsSpawnText(t *testing.T) {
	var got tools.TaskSpec
	runner := &SubagentRunner{
		Cfg: testCfg(), Render: fakeRender{},
		Spawn: func(_ context.Context, sp tools.TaskSpec) (string, string, error) {
			got = sp
			return "  retry\nflaky CI  \n", "job9", nil
		},
		IssueContext: func(context.Context, string, int) (string, error) { return "ISSUE", nil },
	}
	rc := RunContext{Worktree: t.TempDir(), ArtifactDir: t.TempDir()}
	answer, session, err := runner.Run(context.Background(), "oracle", oracleTask, rc)
	if err != nil || answer != "retry\nflaky CI" || session != "job9" || !strings.Contains(got.Task, "rendered ask") || strings.Contains(got.Task, "MUST write") {
		t.Fatalf("answer %q session %q err %v task %q", answer, session, err, got.Task)
	}
}

// With an open PR, a goto to a state that ends the run, or to a state of the
// oracle agent, goes to a human.
func TestOracle_OpenPRGotoEndingStateNeedsHuman(t *testing.T) {
	cases := []struct{ answer, state string }{
		{"goto findings", "findings"},
		{"goto recover", "recover"},
	}
	for _, c := range cases {
		t.Run(c.answer, func(t *testing.T) {
			wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
				"code":     {Agent: "coder", MaxVisits: 1, On: map[string]string{"done": "code"}},
				"findings": {Check: "findings.sh", On: map[string]string{"done": "end", "default": "end"}},
				"recover":  {Agent: "oracle", Task: "recover", On: map[string]string{"stop": "end", "default": "ask"}},
				"ask":      {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
				"end":      {End: true},
			}}
			f := newOracleFixture(wf, c.answer+"\nthe work is finished")
			f.st.PR = 7
			if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
				t.Fatalf("err = %v, want ErrPaused", err)
			}
			if f.st.Status != "paused" || f.st.Current != "ask" {
				t.Fatalf("status %q current %q, want paused at ask", f.st.Status, f.st.Current)
			}
			want := "The oracle proposes the state " + c.state + " while the PR is open, a human must confirm: the work is finished"
			if !strings.Contains(f.st.Ask.Message, want) {
				t.Fatalf("message %q", f.st.Ask.Message)
			}
		})
	}
}

// Without a PR, a goto to merge still goes to a human, with a reason that does
// not mention the PR.
func TestOracle_OracleGotoMergeWithoutPRNeedsHuman(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "recover", States: map[string]State{
		"merge":   {Check: "merge.sh", On: map[string]string{"merged": "end", "default": "end"}},
		"recover": {Agent: "oracle", Task: "recover", On: map[string]string{"stop": "end", "default": "ask"}},
		"ask":     {Ask: "The run needs a human decision.", On: map[string]string{"retry": "recover", "stop": "end"}},
		"end":     {End: true},
	}}
	f := newOracleFixture(wf, "goto:merge the work is ready")
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if f.st.Status != "paused" || f.st.Current != "ask" {
		t.Fatalf("status %q current %q, want paused at ask", f.st.Status, f.st.Current)
	}
	want := "The oracle proposes goto:merge, a human must confirm: the work is ready"
	if !strings.Contains(f.st.Ask.Message, want) || strings.Contains(f.st.Ask.Message, "PR is open") {
		t.Fatalf("message %q", f.st.Ask.Message)
	}
}

func TestOracle_GotoStatesLeaveOutBlockedStates(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":     {Agent: "coder", On: map[string]string{"done": "merge"}},
		"merge":    {Check: "merge.sh", On: map[string]string{"merged": "end", "default": "end"}},
		"findings": {Check: "findings.sh", On: map[string]string{"done": "end", "default": "end"}},
		"ask":      {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
		"end":      {End: true},
	}}
	st := newRun("")
	if got := gotoStates(wf, st); got != "code, findings" {
		t.Fatalf("without a PR: %q, want \"code, findings\"", got)
	}
	st.PR = 7
	if got := gotoStates(wf, st); got != "code" {
		t.Fatalf("with an open PR: %q, want \"code\"", got)
	}
}

func TestOracle_StopWithOpenPRNeedsHuman(t *testing.T) {
	f := newOracleFixture(askWF(false), "stop\nall work is done")
	f.st.PR = 7
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if f.st.Status != "paused" || f.st.Current != "ask" {
		t.Fatalf("status %q current %q, want paused at ask", f.st.Status, f.st.Current)
	}
	if !strings.Contains(f.st.Ask.Message, "The oracle proposes stop, a human must confirm: all work is done") {
		t.Fatalf("message %q", f.st.Ask.Message)
	}
	steps := oracleSteps(f.st)
	if len(steps) != 1 || steps[0].Key != "stop" {
		t.Fatalf("oracle steps %+v", steps)
	}
}

// The key line of the pause answer may come after a line of prose, and goto
// takes a note as the agent route does.
func TestOracle_KeyLineAfterProseAndNote(t *testing.T) {
	f := newOracleFixture(askWF(false), "The worker is stuck.\ngoto:code wait for the fixer")
	_ = f.r.Run(context.Background(), f.st)
	steps := oracleSteps(f.st)
	if len(steps) == 0 || steps[0].Key != "goto:code wait for the fixer" || steps[0].To != "code" {
		t.Fatalf("oracle steps %+v, want a goto:code step with its note to code first", steps)
	}
	if !strings.Contains(strings.Join(f.notices, "\n"), "oracle answered goto:code wait for the fixer:") {
		t.Fatalf("notices %q", f.notices)
	}
}

func TestOracle_StopWithoutPREnds(t *testing.T) {
	f := newOracleFixture(askWF(false), "stop\nno work left")
	if err := f.r.Run(context.Background(), f.st); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if f.st.Status != "done" {
		t.Fatalf("status %q, want done", f.st.Status)
	}
}

func TestOracle_InvalidAnswerEscalates(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		err    error
		want   string
	}{
		{"unknown word", "foo\nreason", nil, `The oracle answer is not valid: "foo"`},
		{"goto unknown state", "goto nowhere\nreason", nil, `The oracle answer is not valid: "goto:nowhere"`},
		{"goto ask state", "goto ask\nreason", nil, `The oracle answer is not valid: "goto:ask"`},
		{"ask escalates", "ask\nthe run needs a human", nil, "The oracle asks a human: the run needs a human"},
		{"oracle error", "retry\nreason", errors.New("boom"), "The oracle failed: boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newOracleFixture(askWF(false), c.answer)
			f.oa.err = c.err
			if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
				t.Fatalf("err = %v, want ErrPaused", err)
			}
			if f.st.Status != "paused" || f.st.Current != "ask" {
				t.Fatalf("status %q current %q", f.st.Status, f.st.Current)
			}
			if !strings.Contains(f.st.Ask.Message, c.want) {
				t.Fatalf("message %q, want %q", f.st.Ask.Message, c.want)
			}
			steps := oracleSteps(f.st)
			if len(steps) != 1 {
				t.Fatalf("oracle steps %d, want 1", len(steps))
			}
		})
	}
}

func TestOracle_ErrorStepKeepsErrorText(t *testing.T) {
	f := newOracleFixture(askWF(false))
	f.oa.err = errors.New("boom")
	_ = f.r.Run(context.Background(), f.st)
	steps := oracleSteps(f.st)
	if len(steps) != 1 || steps[0].Key != "error" || steps[0].Error != "boom" {
		t.Fatalf("oracle steps %+v", steps)
	}
}

func TestOracle_MayAnswerRules(t *testing.T) {
	limit := func(n *int) *Runner {
		wf := askWF(false)
		wf.Defaults.OracleAnswers = n
		return &Runner{WF: wf, Agents: &oracleAgents{}}
	}
	one := 1
	plain := limit(nil)
	noAgents := &Runner{WF: askWF(false)}
	ask := State{Ask: "x", On: map[string]string{"retry": "code"}}
	human := State{Ask: "x", Human: true, On: map[string]string{"retry": "code"}}
	pause := func(history ...Step) *RunState {
		return &RunState{Ask: &Ask{Message: "m"}, History: history}
	}
	cases := []struct {
		name string
		r    *Runner
		s    State
		st   *RunState
		want bool
	}{
		{"plain pause", plain, ask, pause(), true},
		{"no agent runner", noAgents, ask, pause(), false},
		{"human state", plain, human, pause(), false},
		{"workflow proposal", plain, ask, &RunState{Ask: &Ask{Proposal: "/p"}}, false},
		{"after an oracle step", plain, ask, pause(Step{Kind: "ask", Role: "oracle"}), false},
		{"after a protected merge", plain, ask, pause(Step{Kind: "check", State: "merge", Key: "protected"}), false},
		{"after a merged check", plain, ask, pause(Step{Kind: "check", State: "merge", Key: "merged"}), true},
		{"limit reached", limit(&one), ask, pause(Step{Kind: "ask", Role: "oracle"}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.oracleMayAnswer(c.st, c.s); got != c.want {
				t.Fatalf("oracleMayAnswer = %v, want %v", got, c.want)
			}
		})
	}
}

func TestParseOracleAnswer(t *testing.T) {
	cases := []struct {
		in, key, note, reason string
		ok                    bool
	}{
		{"retry\nthe worker missed it", "retry", "", "the worker missed it", true},
		{"\n  retry use X  \n\nline two\nline three\n", "retry", "use X", "line two line three", true},
		{"`retry`\n**reason here**", "retry", "", "reason here", true},
		{"stop", "stop", "", "", true},
		{"stop.\nall work is done", "stop", "", "all work is done", true},
		{"goto:ci wait for CI", "goto:ci", "wait for CI", "", true},
		{"goto ci wait for CI", "goto:ci", "wait for CI", "", true},
		{"GOTO:CI", "goto:ci", "", "", true},
		{"The CI is flaky.\ngoto:ci wait for CI", "goto:ci", "wait for CI", "The CI is flaky.", true},
		{"ask the origin is gone", "ask", "the origin is gone", "", true},
		{"prose only\nmore prose", "", "", "prose only more prose", false},
		{"goto:\ngoto", "", "", "goto: goto", false},
		{"  \n\n ", "", "", "", false},
	}
	for _, c := range cases {
		key, note, reason, ok := parseOracleAnswer(c.in)
		if key != c.key || note != c.note || reason != c.reason || ok != c.ok {
			t.Fatalf("parseOracleAnswer(%q) = %q, %q, %q, %v; want %q, %q, %q, %v",
				c.in, key, note, reason, ok, c.key, c.note, c.reason, c.ok)
		}
	}
}

func TestParse_HumanAndOracleAnswers(t *testing.T) {
	data := []byte(`{"name":"demo","start":"a","defaults":{"oracle_answers":0},
		"states":{"a":{"check":"x.sh","on":{"go":"ask"}},"ask":{"ask":"x","human":true,"on":{"retry":"a"}}}}`)
	wf, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Defaults.OracleAnswers == nil || *wf.Defaults.OracleAnswers != 0 {
		t.Fatalf("oracle_answers = %v, want 0", wf.Defaults.OracleAnswers)
	}
	if !wf.States["ask"].Human {
		t.Fatal("human not parsed")
	}
}

func TestValidate_HumanOnlyInAskState(t *testing.T) {
	wf := &Workflow{Name: "demo", Description: "d", Start: "a", States: map[string]State{
		"a":   {Agent: "coder", Human: true, On: map[string]string{"done": "end"}},
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "human is only allowed in an ask state") {
		t.Fatalf("errs %v", errs)
	}
}

func TestValidate_OracleAnswersNotNegative(t *testing.T) {
	neg := -1
	wf := &Workflow{Name: "demo", Description: "d", Start: "end", Defaults: Defaults{OracleAnswers: &neg}, States: map[string]State{
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "oracle_answers must be 0 or more, got -1") {
		t.Fatalf("errs %v", errs)
	}
}

func TestOracleTaskProblem(t *testing.T) {
	zero := 0
	ask := State{Ask: "need a human"}
	end := State{End: true}
	cases := []struct {
		name     string
		states   map[string]State
		defaults Defaults
		file     bool
		want     bool // true: a problem is expected
	}{
		{"missing file", map[string]State{"ask": ask, "end": end}, Defaults{}, false, true},
		{"file present", map[string]State{"ask": ask, "end": end}, Defaults{}, true, false},
		{"human ask state", map[string]State{"ask": {Ask: "x", Human: true}, "end": end}, Defaults{}, false, false},
		{"oracle off", map[string]State{"ask": ask, "end": end}, Defaults{OracleAnswers: &zero}, false, false},
		{"no ask state", map[string]State{"end": end}, Defaults{}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.file {
				if err := os.MkdirAll(filepath.Join(dir, "tasks"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "tasks", "ask.md"), []byte("ask"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			wf := &Workflow{Name: "demo", Description: "d", Start: "end", Defaults: c.defaults, States: c.states}
			got := oracleTaskProblem(wf, dir)
			if (got != "") != c.want {
				t.Fatalf("problem = %q, want a problem: %v", got, c.want)
			}
			if c.want && !strings.Contains(got, `tasks/ask.md is missing`) {
				t.Errorf("problem = %q, want it to name tasks/ask.md", got)
			}
		})
	}
}

// A guarded state keeps its guard under any name (#562): the oracle goto to it
// goes to a human, and the merge name is not guarded in such a workflow.
func TestOracle_GuardedStateWithOtherNameNeedsHuman(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":    {Agent: "coder", MaxVisits: 1, On: map[string]string{"done": "code"}},
		"publish": {Check: "publish.sh", Guard: true, On: map[string]string{"done": "end", "default": "end"}},
		"ask":     {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
		"end":     {End: true},
	}}
	f := newOracleFixture(wf, "goto:publish\nthe work is ready")
	if err := f.r.Run(context.Background(), f.st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if f.st.Status != "paused" || f.st.Current != "ask" {
		t.Fatalf("status %q current %q, want paused at ask", f.st.Status, f.st.Current)
	}
	want := "The oracle proposes the state publish, a human must confirm: the work is ready"
	if !strings.Contains(f.st.Ask.Message, want) {
		t.Fatalf("message %q", f.st.Ask.Message)
	}
	if isGuarded(wf, "merge") {
		t.Fatalf("merge is guarded in a workflow with a guard flag")
	}
}

// A chain of states that ends the run is blocked while the PR is open, and a
// chain that does not end the run is not (#562).
func TestOracle_ChainThatEndsRunNeedsHumanWithOpenPR(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "code", States: map[string]State{
		"code":   {Agent: "coder", On: map[string]string{"done": "push"}},
		"push":   {Check: "push.sh", On: map[string]string{"ok": "finish", "default": "finish"}},
		"finish": {Check: "finish.sh", On: map[string]string{"done": "end", "default": "end"}},
		"review": {Check: "review.sh", On: map[string]string{"ok": "review", "default": "end"}},
		"end":    {End: true},
		"ask":    {Ask: "The run needs a human decision.", On: map[string]string{"retry": "code", "stop": "end"}},
	}}
	st := newRun("")
	if oracleBlocked(wf, st, "goto:push", "push") {
		t.Fatalf("goto push blocked without a PR")
	}
	st.PR = 7
	if !oracleBlocked(wf, st, "goto:push", "push") {
		t.Fatalf("goto push (push -> finish -> end) not blocked with an open PR")
	}
	if !oracleBlocked(wf, st, "goto:finish", "finish") {
		t.Fatalf("goto finish not blocked with an open PR")
	}
	// review loops on itself on ok, so it does not end the run on every path.
	if oracleBlocked(wf, st, "goto:review", "review") {
		t.Fatalf("goto review blocked: it does not always end the run")
	}
}

// In the release template an oracle goto:tag goes to a human, with or without an
// open PR (#562). The release merge stays guarded too.
func TestOracle_ReleaseTagGotoNeedsHuman(t *testing.T) {
	wf := initWF(t, t.TempDir(), "release", "release")
	for _, pr := range []int{0, 7} {
		st := newRun("")
		st.PR = pr
		if !oracleBlocked(wf, st, "goto:tag", "tag") {
			t.Fatalf("PR %d: goto:tag not blocked", pr)
		}
		if !oracleBlocked(wf, st, "goto:merge", "merge") {
			t.Fatalf("PR %d: goto:merge not blocked", pr)
		}
		if strings.Contains(gotoStates(wf, st), "tag") {
			t.Fatalf("PR %d: gotoStates %q lists tag", pr, gotoStates(wf, st))
		}
	}
}
