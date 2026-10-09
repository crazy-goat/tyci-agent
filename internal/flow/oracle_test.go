package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// oracleAgents runs the oracle role with scripted answers. Other roles go to inner.
type oracleAgents struct {
	inner   *fakeAgents
	answers []string
	err     error
	calls   []RunContext
	tasks   []string
}

func (o *oracleAgents) Run(ctx context.Context, role, task string, rc RunContext) (string, string, error) {
	if role != "oracle" {
		return o.inner.Run(ctx, role, task, rc)
	}
	o.calls = append(o.calls, rc)
	o.tasks = append(o.tasks, task)
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
		{"goto unknown state", "goto nowhere\nreason", nil, `The oracle answer is not valid: "goto nowhere"`},
		{"goto ask state", "goto ask\nreason", nil, `The oracle answer is not valid: "goto ask"`},
		{"stop with text", "stop now\nreason", nil, `The oracle answer is not valid: "stop now"`},
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

func TestParseAskAnswer(t *testing.T) {
	cases := []struct {
		in, answer, reason string
	}{
		{"retry\nthe worker missed it", "retry", "the worker missed it"},
		{"\n  retry use X  \n\nline two\nline three\n", "retry use X", "line two line three"},
		{"`retry`\n**reason here**", "retry", "reason here"},
		{"stop", "stop", "no reason given"},
		{"  \n\n ", "", "no reason given"},
	}
	for _, c := range cases {
		answer, reason := parseAskAnswer(c.in)
		if answer != c.answer || reason != c.reason {
			t.Fatalf("parseAskAnswer(%q) = %q, %q; want %q, %q", c.in, answer, reason, c.answer, c.reason)
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
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
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
	wf := &Workflow{Name: "demo", Start: "end", Defaults: Defaults{OracleAnswers: &neg}, States: map[string]State{
		"end": {End: true},
	}}
	errs := validateStructure(wf)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "oracle_answers must be 0 or more, got -1") {
		t.Fatalf("errs %v", errs)
	}
}
