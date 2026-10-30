package flow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/tyciconfig"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

func semiWorkflow() *Workflow {
	return &Workflow{Name: "demo", Description: "Test human review", Start: "code", Mode: tyciconfig.ModeSemi, States: map[string]State{
		"code":         {Agent: "worker", MaxVisits: 3, On: map[string]string{"done": "review", "default": "ask"}},
		"review":       {Agent: "review", On: map[string]string{"ACCEPT": "push", "CHANGES": "code"}},
		"human_review": {HumanReview: true, On: map[string]string{"accept": "push", "fix": "code", "decline": "code"}},
		"push":         {Check: "push.sh", On: map[string]string{"ok": "end"}},
		"ask":          {Ask: "Help", Human: true, On: map[string]string{"stop": "end"}},
		"end":          {End: true},
	}}
}

func TestModeDefaultsToAuto(t *testing.T) {
	wf, err := Parse([]byte(`{"description":"d","start":"end","states":{"end":{"end":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	mode, err := wf.Mode.Resolve()
	if err != nil || mode != tyciconfig.ModeAuto {
		t.Fatalf("mode = %q, %v", mode, err)
	}
}

func TestModeRejectsUnknownValue(t *testing.T) {
	if _, err := Parse([]byte(`{"mode":"manual"}`)); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestReviewAcceptGoesToPushInAuto(t *testing.T) {
	wf := semiWorkflow()
	wf.Mode = tyciconfig.ModeAuto
	agents := &fakeAgents{keys: map[string][]string{"code": {"done"}, "review": {"ACCEPT"}}}
	checks := &fakeChecks{keys: map[string][]string{"push.sh": {"ok"}}}
	st := newRun("")
	if err := (&Runner{WF: wf, Agents: agents, Checks: checks}).Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" || !slices.Equal(historyStates(st), []string{"code", "review", "push"}) {
		t.Fatalf("state = %+v", st)
	}
}

func TestReviewAcceptGoesToHumanReviewInSemi(t *testing.T) {
	wf := semiWorkflow()
	agents := &fakeAgents{keys: map[string][]string{"code": {"done"}, "review": {"ACCEPT"}}}
	st := newRun("")
	err := (&Runner{WF: wf, Agents: agents}).Run(context.Background(), st)
	if !errors.Is(err, ErrPaused) || st.Status != "paused" || st.Current != "human_review" || st.Ask == nil {
		t.Fatalf("state = %+v, error = %v", st, err)
	}
	if !slices.Equal(agents.calls, []string{"code", "review"}) {
		t.Fatalf("agents = %v: human review must not call the oracle", agents.calls)
	}
}

func TestValidateSemiNeedsHumanReviewOutcomes(t *testing.T) {
	for _, missing := range []string{"state", "accept", "fix", "decline"} {
		t.Run(missing, func(t *testing.T) {
			wf := semiWorkflow()
			if missing == "state" {
				delete(wf.States, "human_review")
			} else {
				delete(wf.States["human_review"].On, missing)
			}
			errs := validateStructure(wf)
			if len(errs) == 0 || !strings.Contains(errors.Join(errs...).Error(), "human_review") {
				t.Fatalf("errors = %v", errs)
			}
		})
	}
	if errs := validateStructure(semiWorkflow()); len(errs) != 0 {
		t.Fatal(errs)
	}
}

func TestHumanReviewSurvivesRestartAndFixPreservesVisits(t *testing.T) {
	wf := semiWorkflow()
	dir := filepath.Join(t.TempDir(), "run")
	store := &Store{Dir: dir}
	agents := &fakeAgents{keys: map[string][]string{"code": {"done", "done", "done"}, "review": {"ACCEPT"}}}
	st := newRun("")
	r := &Runner{WF: wf, Agents: agents, Store: store}
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	// New state and runner instances simulate a fresh process. A later config
	// change to auto must not remove the gate of this saved semi run.
	st, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	wf.Mode = tyciconfig.ModeAuto
	r = &Runner{WF: wf, Agents: agents, Store: store}
	if err := r.Continue(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
		t.Fatalf("restart = %+v, %v", st, err)
	}
	for want := 2; want <= 3; want++ {
		if err := r.Resume(context.Background(), st, "fix handle the nil case"); !errors.Is(err, ErrPaused) {
			t.Fatalf("fix %d: %v", want, err)
		}
		if st.Current != "human_review" || st.Visits["code"] != want || st.Visits["review"] != 1 {
			t.Fatalf("fix %d state = %+v", want, st)
		}
		if rc := agents.rcs[len(agents.rcs)-1]; rc.Note != "handle the nil case" || rc.Visit != want {
			t.Fatalf("worker context = %+v", rc)
		}
		st, err = Load(dir)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Resume(context.Background(), st, "fix"); !errors.Is(err, ErrPaused) || st.Current != "human_review" || !strings.Contains(st.Ask.Message, "max_visits:code") {
		t.Fatalf("limit state = %+v, %v", st, err)
	}
	if !slices.Equal(agents.calls, []string{"code", "review", "code", "code"}) {
		t.Fatalf("agent calls = %v", agents.calls)
	}
}

func TestHumanReviewBuiltinFixLimitNeverCallsOracle(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	if _, err := Init("issue-to-merge", "", project); err != nil {
		t.Fatal(err)
	}
	wf, _, err := Lookup("issue-to-merge", home, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if wf.States["ask"].Human {
		t.Fatal("regression must exercise the builtin's oracle-capable ask")
	}
	wf.Mode = tyciconfig.ModeSemi
	info := RepoInfo{Home: home, Root: project, Repo: "o/r", Trusted: true}
	dir := RunDir(home, info.Key(), "1")
	store := &Store{Dir: dir}
	agents := &fakeAgents{keys: map[string][]string{"code": {"done", "done", "done"}, "review": {"ACCEPT"}, "findings": {"done"}}}
	st := newRun("code")
	st.Run, st.Workflow = "1", "issue-to-merge"
	r := &Runner{WF: wf, Agents: agents, Store: store}
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		st, err = Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Resume(context.Background(), st, "fix"); !errors.Is(err, ErrPaused) {
			t.Fatal(err)
		}
	}
	st, err = Load(dir)
	if err != nil || st.Current != "human_review" || st.Visits["code"] != 3 || !strings.Contains(st.Ask.Message, "max_visits:code") || !slices.Equal(agents.calls, []string{"code", "review", "code", "code"}) {
		t.Fatalf("exhausted state = %+v, agents = %v, %v", st, agents.calls, err)
	}
	m := NewManager(nil, nil)
	m.Info = func() (RepoInfo, error) { return info, nil }
	pool := &Pool{}
	pool.Join(m)
	m.SetWorkers(1)
	if m.usedSlots().total != 1 {
		t.Fatal("exhausted human review lost its worker slot after restart")
	}
	checks := &fakeChecks{keys: map[string][]string{
		"checks/lock.sh": {"ok"}, "checks/rebase.sh": {"ok"}, "checks/post_review.sh": {"skip"},
		"checks/ci_wait.sh": {"green"}, "checks/fetch_comments.sh": {"none"}, "checks/merge.sh": {"merged"},
	}}
	m.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
		return &Runner{WF: wf, Agents: agents, Checks: checks, Store: store}
	}
	events := make(chan RunEvent, 8)
	unsub := m.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	if err := m.Resume("1", "accept"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Status == "done" {
				after, err := Load(dir)
				if err != nil || after.Visits["code"] != 3 || !slices.Equal(agents.calls, []string{"code", "review", "code", "code", "findings"}) {
					t.Fatalf("accepted state = %+v, agents = %v, %v", after, agents.calls, err)
				}
				return
			}
			if ev.Status == "failed" {
				t.Fatalf("accept failed: %+v", ev)
			}
		case <-timer.C:
			t.Fatal("accept did not finish")
		}
	}
}

func TestHumanReviewAcceptContinuesSavedRoute(t *testing.T) {
	wf := semiWorkflow()
	st := newRun("human_review")
	st.Status = "paused"
	checks := &fakeChecks{keys: map[string][]string{"push.sh": {"ok"}}}
	r := &Runner{WF: wf, Checks: checks}
	if err := r.Resume(context.Background(), st, "goto push"); err == nil {
		t.Fatal("a human review must select one of its outcomes")
	}
	if err := r.Resume(context.Background(), st, "accept"); err != nil || st.Status != "done" {
		t.Fatalf("accept = %+v, %v", st, err)
	}
}

func TestHumanReviewDeclineUsesMappedRoute(t *testing.T) {
	wf := semiWorkflow()
	st := newRun("human_review")
	st.Status = "paused"
	st.Visits = map[string]int{"code": 1, "review": 1}
	agents := &fakeAgents{keys: map[string][]string{"code": {"done"}}}
	r := &Runner{WF: wf, Agents: agents}
	if err := r.Resume(context.Background(), st, "decline use the existing helper"); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	if st.Current != "human_review" || st.Visits["code"] != 2 || st.Visits["review"] != 1 || agents.rcs[0].Note != "use the existing helper" {
		t.Fatalf("declined state = %+v, worker = %+v", st, agents.rcs)
	}
	// The outcome belongs to the workflow graph; custom definitions may end.
	wf.States["human_review"].On["decline"] = "end"
	if err := r.Resume(context.Background(), st, "decline"); err != nil || st.Status != "done" {
		t.Fatalf("terminal decline = %+v, %v", st, err)
	}
}

func TestHumanReviewTerminalOutcomeIsNotReportedSkipped(t *testing.T) {
	var notice string
	m := &Manager{Notify: func(s string) { notice = s }}
	m.notify(&RunState{Run: "1", Status: "done", History: []Step{{Kind: "human_review", Key: "decline", To: "end"}}}, semiWorkflow())
	if !strings.Contains(notice, "stopped: no PR") {
		t.Fatalf("notice = %s", notice)
	}
}

type humanFixInterruptStore struct {
	store   *Store
	entered bool
	err     error
}

func (s humanFixInterruptStore) Save(st *RunState) error {
	if err := s.store.Save(st); err != nil {
		return err
	}
	if st.Current == "code" && st.EntryPending != s.entered && len(st.History) > 0 && st.History[len(st.History)-1].Kind == "human_review" {
		return s.err
	}
	return nil
}

func TestHumanReviewFixSurvivesRestartDuringCodeEntry(t *testing.T) {
	for _, entered := range []bool{false, true} {
		t.Run(map[bool]string{false: "before visit save", true: "after visit save"}[entered], func(t *testing.T) {
			wf := semiWorkflow()
			store := &Store{Dir: t.TempDir()}
			st := newRun("human_review")
			st.Mode, st.Status = tyciconfig.ModeSemi, "paused"
			st.Visits = map[string]int{"code": 1, "review": 1}
			interrupted := errors.New("simulated process exit")
			r := &Runner{WF: wf, Store: humanFixInterruptStore{store: store, entered: entered, err: interrupted}}
			if err := r.Resume(context.Background(), st, "fix add a regression"); !errors.Is(err, interrupted) {
				t.Fatalf("interruption = %v", err)
			}
			st, err := Load(store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if st.Current != "code" || st.EntryPending == entered {
				t.Fatalf("saved state = %+v", st)
			}
			agents := &fakeAgents{keys: map[string][]string{"code": {"done"}}}
			r = &Runner{WF: wf, Store: store, Agents: agents}
			if err := r.Continue(context.Background(), st); !errors.Is(err, ErrPaused) || st.Current != "human_review" {
				t.Fatalf("restart = %+v, %v", st, err)
			}
			if st.Visits["code"] != 2 || st.Visits["review"] != 1 || !slices.Equal(agents.calls, []string{"code"}) || agents.rcs[0].Note != "add a regression" {
				t.Fatalf("restart state = %+v, agents = %+v", st, agents)
			}
		})
	}
}

func TestConfiguredSemiValidationAndPrepare(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tyci", "config.json"), []byte(`{"default_model":"p/m","workflows":{"issue-to-merge":{"mode":"semi"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init("issue-to-merge", "", project); err != nil {
		t.Fatal(err)
	}
	info := RepoInfo{Home: home, Root: project, Trusted: true}
	if _, problems, err := CheckWorkflow(info, "issue-to-merge"); err != nil || len(problems) != 0 {
		t.Fatalf("check = %v, %v", problems, err)
	}
	// The production resume loader must apply the same per-workflow override.
	wf, err := NewManager(nil, nil).Workflow(info, "issue-to-merge")
	if err != nil || wf.Mode != tyciconfig.ModeSemi {
		t.Fatalf("resume workflow = %+v, %v", wf, err)
	}
	if wf.States["review"].On["ACCEPT"] != "lock" || wf.States["human_review"].On["accept"] != "lock" {
		t.Fatal("template changed the existing post-review path")
	}
	dir := filepath.Join(t.TempDir(), "run")
	d := PrepareDeps{
		Lookup:   func(name string) (*Workflow, string, error) { return Lookup(name, home, project, true) },
		Config:   func() (*flowconfig.Config, error) { return flowconfig.Load(home, project, true) },
		NewRunID: func() (string, error) { return "1", nil },
		AddWorktree: func(context.Context, string, int) (*worktree.Worktree, error) {
			return &worktree.Worktree{Dir: t.TempDir(), Branch: "issue-1"}, nil
		},
		NewStore: func(string) (*Store, error) { return &Store{Dir: dir}, nil },
	}
	st, wf, _, err := PrepareRun(context.Background(), d, PrepareReq{Workflow: "issue-to-merge", Params: []string{"1"}})
	if err != nil || wf.Mode != tyciconfig.ModeSemi || st.Mode != tyciconfig.ModeSemi {
		t.Fatalf("prepared = %+v, %+v, %v", st, wf, err)
	}
	saved, err := Load(dir)
	if err != nil || saved.Mode != tyciconfig.ModeSemi {
		t.Fatalf("saved = %+v, %v", saved, err)
	}
	// Validate the effective config mode, even when workflow.json is still auto.
	path := filepath.Join(project, ".tyci", "workflows", "issue-to-merge", "workflow.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	delete(parsed.States["human_review"].On, "fix")
	data, err = json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, problems, err := CheckWorkflow(info, "issue-to-merge")
	if err != nil || !strings.Contains(strings.Join(problems, "\n"), "human_review") {
		t.Fatalf("invalid semi check = %v, %v", problems, err)
	}
}

func TestHumanReviewManagerRestartAndResume(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	wf := semiWorkflow()
	wf.Mode = tyciconfig.ModeAuto
	data, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	e2eWrite(t, filepath.Join(project, ".tyci", "workflows", "demo", "workflow.json"), string(data))
	e2eWrite(t, filepath.Join(project, ".tyci", "workflows", "demo", "push.sh"), "#!/bin/sh\n")
	e2eWrite(t, filepath.Join(home, ".tyci", "config.json"), `{"workflows":{"demo":{"mode":"semi"}}}`)
	info := RepoInfo{Home: home, Root: project, Repo: "o/r", Trusted: true}
	dir := RunDir(home, info.Key(), "1")
	st := newRun("human_review")
	st.Run, st.Mode, st.Status = "1", tyciconfig.ModeSemi, "paused"
	st.Ask = &Ask{Message: "The change waits for human review.", Reason: "human_review"}
	st.Visits = map[string]int{"code": 1, "review": 1}
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	notices := make(chan string, 8)
	m := NewManager(func(s string) { notices <- s }, nil)
	m.Info = func() (RepoInfo, error) { return info, nil }
	m.SetWorkers(1)
	prepared := 0
	m.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		prepared++
		return nil, nil, nil, errors.New("preparation reached")
	}
	agents := &fakeAgents{keys: map[string][]string{"code": {"done"}}}
	m.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
		return &Runner{WF: wf, Agents: agents, Checks: &fakeChecks{keys: map[string][]string{"push.sh": {"ok"}}}, Store: &Store{Dir: dir}}
	}
	m.AskUnfinished()
	if notice := <-notices; !strings.Contains(notice, "answers: accept, decline, fix") || strings.Contains(notice, `chose ("resume" or "stop")`) {
		t.Fatalf("startup notice = %s", notice)
	}
	after, err := Load(dir)
	if err != nil || after.Current != "human_review" || after.Status != "paused" {
		t.Fatalf("startup state = %+v, %v", after, err)
	}
	m.adopted = map[string]bool{"1": true}
	m.ReleaseAdopted()
	if _, _, err := m.Start(context.Background(), StartRequest{Workflow: "demo"}); !errors.Is(err, ErrNoSlot) || prepared != 0 {
		t.Fatalf("start with human review slot held = %v, preparations = %d", err, prepared)
	}
	events := make(chan RunEvent, 8)
	unsub := m.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	wait := func(status string) {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case ev := <-events:
				if ev.Status == status {
					return
				}
				if ev.Status == "failed" {
					t.Fatalf("run failed: %+v", ev)
				}
			case <-timer.C:
				t.Fatalf("run did not reach %s", status)
			}
		}
	}
	if err := m.Resume("1", "goto push"); err == nil {
		t.Fatal("manager accepted an outcome that bypasses human review")
	}
	if err := m.Resume("1", "fix add the missing check"); err != nil {
		t.Fatal(err)
	}
	wait("paused")
	after, err = Load(dir)
	if err != nil || after.Current != "human_review" || after.Visits["code"] != 2 || after.Visits["review"] != 1 || !slices.Equal(agents.calls, []string{"code"}) {
		t.Fatalf("fixed state = %+v, agents = %+v, %v", after, agents, err)
	}
	if err := m.Resume("1", "accept"); err != nil {
		t.Fatal(err)
	}
	wait("done")
	if _, _, err := m.Start(context.Background(), StartRequest{Workflow: "demo"}); err == nil || errors.Is(err, ErrNoSlot) || prepared != 1 {
		t.Fatalf("start after slot released = %v, preparations = %d", err, prepared)
	}
}

func TestHumanReviewCrashBeforePauseSaveKeepsGateAndSlot(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	st := newRun("human_review")
	st.Run, st.Mode, st.PID, st.EntryPending = "1", tyciconfig.ModeSemi, deadPID(t), true
	dir := RunDir(home, info.Key(), st.Run)
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	var notice string
	m := &Manager{Info: func() (RepoInfo, error) { return info, nil }, Workflow: func(RepoInfo, string) (*Workflow, error) { return semiWorkflow(), nil }, Notify: func(s string) { notice = s }}
	pool := &Pool{}
	pool.Join(m)
	if m.usedSlots().total != 1 {
		t.Fatal("transition saved before pause lost its worker reservation")
	}
	m.AskUnfinished()
	after, err := Load(dir)
	if err != nil || after.Current != "human_review" || !isHumanReviewPause(after) || !strings.Contains(notice, "answers: accept, decline, fix") {
		t.Fatalf("restarted state = %+v, notice = %s, %v", after, notice, err)
	}
	if err := m.Resume("1", "goto push"); err == nil {
		t.Fatal("restart before the pause save allowed a goto to bypass human review")
	}
}

func TestHumanReviewSlotDoesNotChangeOrdinaryAskAccounting(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	st := newRun("ask")
	st.Run, st.Status, st.Ask = "1", "paused", &Ask{Message: "Need a decision."}
	if err := (&Store{Dir: RunDir(home, info.Key(), st.Run)}).Save(st); err != nil {
		t.Fatal(err)
	}
	prepareReached := errors.New("preparation reached")
	m := &Manager{Info: func() (RepoInfo, error) { return info, nil }, Workflow: func(RepoInfo, string) (*Workflow, error) { return demoWF(), nil }}
	m.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		return nil, nil, nil, prepareReached
	}
	m.SetWorkers(1)
	if _, _, err := m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"2"}}); !errors.Is(err, prepareReached) {
		t.Fatalf("ordinary unadopted ask changed slot accounting: %v", err)
	}
}

func TestHumanReviewSlotPreservesAllAutoAskResumeBehavior(t *testing.T) {
	home := t.TempDir()
	info := RepoInfo{Home: home, Repo: "o/r"}
	st := newRun("ask")
	st.Run, st.Status, st.Ask = "1", "paused", &Ask{Message: "Need a decision."}
	dir := RunDir(home, info.Key(), st.Run)
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Info: func() (RepoInfo, error) { return info, nil }, Workflow: func(RepoInfo, string) (*Workflow, error) { return demoWF(), nil }}
	m.SetWorkers(1)
	// Existing auto ask resumes do not reserve a new slot; preserve that
	// behavior when there is no semi run or human-review reservation.
	m.active = map[string]activeRun{"other": {issue: 2}}
	m.countActive()
	m.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
		return &Runner{WF: wf, Checks: &fakeChecks{keys: map[string][]string{"c.sh": {"ok"}}}, Store: &Store{Dir: dir}}
	}
	events := make(chan RunEvent, 8)
	unsub := m.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	if err := m.Resume("1", "retry"); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Status == "done" {
				return
			}
		case <-timer.C:
			t.Fatal("auto ask did not resume")
		}
	}
}

func TestHumanReviewSlotSurvivesSharedPoolRestart(t *testing.T) {
	home := t.TempDir()
	firstInfo := RepoInfo{Home: home, Repo: "o/first"}
	secondInfo := RepoInfo{Home: home, Repo: "o/second"}
	st := newRun("human_review")
	st.Run, st.Repo, st.Mode, st.Status = "1", firstInfo.Repo, tyciconfig.ModeSemi, "paused"
	st.Ask = &Ask{Reason: "human_review"}
	firstDir := RunDir(home, firstInfo.Key(), st.Run)
	if err := (&Store{Dir: firstDir}).Save(st); err != nil {
		t.Fatal(err)
	}
	// Fresh managers recover reservations from saved states when they join.
	first := &Manager{Info: func() (RepoInfo, error) { return firstInfo, nil }, Workflow: func(RepoInfo, string) (*Workflow, error) { return semiWorkflow(), nil }}
	second := &Manager{Info: func() (RepoInfo, error) { return secondInfo, nil }, Workflow: func(RepoInfo, string) (*Workflow, error) { return demoWF(), nil }}
	prepareReached := errors.New("preparation reached")
	second.Prepare = func(context.Context, RepoInfo, StartRequest) (*RunState, *Workflow, []string, error) {
		return nil, nil, nil, prepareReached
	}
	pool := &Pool{}
	pool.Join(first)
	pool.Join(second)
	second.SetWorkers(1)
	start := func() error {
		_, _, err := second.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"2"}})
		return err
	}
	if err := start(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("other repository bypassed saved review reservation: %v", err)
	}
	// A stale/startup resume in another repo must respect that slot too.
	other := newRun("ask")
	other.Run, other.Repo, other.Status = "2", secondInfo.Repo, "paused"
	other.Ask = &Ask{Reason: "resume:c"}
	if err := (&Store{Dir: RunDir(home, secondInfo.Key(), other.Run)}).Save(other); err != nil {
		t.Fatal(err)
	}
	if err := second.Resume("2", "resume"); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("startup resume bypassed human review reservation: %v", err)
	}
	if err := second.Resume("2", "retry"); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("ordinary ask resume bypassed human review reservation: %v", err)
	}
	release := make(chan struct{})
	first.NewRunner = func(_ RepoInfo, wf *Workflow, _ *RunState) *Runner {
		return &Runner{WF: wf, Checks: &gatedChecks{release: release, key: "ok"}, Store: &Store{Dir: firstDir}}
	}
	events := make(chan RunEvent, 8)
	unsub := first.Subscribe(func(ev RunEvent) { events <- ev })
	defer unsub()
	if err := first.Resume("1", "accept"); err != nil {
		t.Fatal(err)
	}
	if first.usedSlots().total != 1 {
		t.Fatalf("resuming a held reservation counts %d slots, want 1", first.usedSlots().total)
	}
	if err := start(); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("other repository bypassed active review reservation: %v", err)
	}
	if err := second.Resume("2", "retry"); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("ordinary ask resume bypassed active review reservation: %v", err)
	}
	close(release)
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Status == "done" {
				if err := start(); !errors.Is(err, prepareReached) {
					t.Fatalf("review slot was not released: %v", err)
				}
				return
			}
		case <-timer.C:
			t.Fatal("accepted run did not finish")
		}
	}
}
