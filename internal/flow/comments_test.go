package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunner_PostReviewFailLeavesHistoryAndNotice(t *testing.T) {
	var notices []string
	r := &Runner{
		WF:     builtinWF(t),
		Checks: &fakeChecks{keys: map[string][]string{"checks/post_review.sh": {"fail"}, "checks/ci_wait.sh": {"green"}, "checks/fetch_comments.sh": {"none"}, "checks/merge.sh": {"merged"}}},
		Agents: &fakeAgents{keys: map[string][]string{"findings": {"done"}}},
		Warn:   func(m string) { notices = append(notices, m) },
	}
	st := newRun("post_review")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	h := st.History[0]
	if h.State != "post_review" || h.To != "ci" || !slices.Contains(h.Warnings, "review_post_failed") {
		t.Errorf("step = %+v", h)
	}
	if !slices.ContainsFunc(notices, func(m string) bool { return strings.Contains(m, "posting the review") }) {
		t.Errorf("notices = %v", notices)
	}
}

func TestRunner_CommentsGoBackToCode(t *testing.T) {
	r := &Runner{
		WF: builtinWF(t),
		Checks: &fakeChecks{keys: map[string][]string{
			"checks/fetch_comments.sh": {"new", "new", "new", "new"},
			"checks/lock.sh":           {"ok", "ok", "ok"}, "checks/rebase.sh": {"ok", "ok", "ok"},
			"checks/post_review.sh": {"ok", "ok", "ok"},
			"checks/ci_wait.sh":     {"green", "green", "green"},
		}},
		Agents: &fakeAgents{keys: map[string][]string{
			"code": {"done", "done", "done"}, "review": {"ACCEPT", "ACCEPT", "ACCEPT"},
		}},
	}
	st := newRun("comments")
	if err := r.Run(context.Background(), st); err != ErrPaused {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if st.Visits["code"] != 3 || st.Ask == nil || st.Ask.Reason != "max_visits:code" {
		t.Errorf("visits = %v, ask = %+v", st.Visits, st.Ask)
	}
}

type idChecker struct{ dir string }

func (c idChecker) Run(_ context.Context, _ State, env []string, _ string) (string, CheckResult, error) {
	if !slices.Contains(env, "TYCI_LAST_COMMENT_ID=42") {
		return "", CheckResult{}, os.ErrInvalid
	}
	err := os.WriteFile(filepath.Join(c.dir, "last_comment_id"), []byte("57\n"), 0o644)
	return "none", CheckResult{}, err
}

func TestRunner_LastCommentIDSurvivesResume(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{WF: builtinWF(t), Checks: idChecker{dir}, RunDir: dir}
	st := newRun("comments")
	st.LastCommentID = 42
	r.WF.States["merge"] = State{End: true}
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.LastCommentID != 57 {
		t.Fatalf("LastCommentID = %d", st.LastCommentID)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var back RunState
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	env := buildCheckEnv(&back, State{}, dir, "main")
	if !slices.Contains(env, "TYCI_LAST_COMMENT_ID=57") {
		t.Errorf("env = %v", env)
	}
}

func TestWorkerTask_IncludesCommentsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "comments.md"), []byte("## Comment 1 by alice (u)\nrename X\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &spawnRec{result: "ok"}
	if _, _, err := newRunner(s).Run(context.Background(), "worker", "", RunContext{RunDir: dir, Worktree: dir}); err != nil {
		t.Fatal(err)
	}
	if got := s.specs[0].Task; !strings.Contains(got, "rename X") {
		t.Errorf("task = %q", got)
	}
}

func TestReadLastCommentID_ReviewID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "last_review_comment_id"), []byte("9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &RunState{LastReviewCommentID: 4}
	readLastCommentID(st, dir)
	if st.LastReviewCommentID != 9 || st.LastCommentID != 0 {
		t.Errorf("%+v", st)
	}
	env := buildCheckEnv(st, State{}, dir, "main")
	if !slices.Contains(env, "TYCI_LAST_REVIEW_COMMENT_ID=9") {
		t.Errorf("env = %v", env)
	}
}

func TestManager_PostReviewFailReachesNotify(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "fail"})
	prep := e.m.Prepare
	e.m.Prepare = func(ctx context.Context, info RepoInfo, req StartRequest) (*RunState, *Workflow, []string, error) {
		st, _, w, err := prep(ctx, info, req)
		wf := &Workflow{Name: "demo", Start: "c", States: map[string]State{
			"c":   {Check: "checks/post_review.sh", On: map[string]string{"fail": "end"}},
			"end": {End: true},
		}}
		return st, wf, w, err
	}
	if _, _, err := e.m.Start(context.Background(), StartRequest{Issue: 1}); err != nil {
		t.Fatal(err)
	}
	if n := e.notice(t); !strings.Contains(n, "posting the review") {
		t.Errorf("notice = %q", n)
	}
	// Wait for the run to end so the run goroutine stops writing to TempDir.
	e.notice(t)
}
