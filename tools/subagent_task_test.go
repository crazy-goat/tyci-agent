package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunSubagentTask_UsesDirAndSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("here"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotDir string
	var gotRel string
	r := &recordingRunner{}
	run := &ctxRunner{rec: r, fn: func(ctx context.Context) {
		gotDir = Workdir(ctx)
		gotRel = resolvePath(ctx, "marker.txt")
	}}
	out, id1, err := runSubagentTask(context.Background(), run, TaskSpec{Task: "x", Model: "m", SystemPrompt: "SYS", Dir: dir})
	if err != nil || out != "ok" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if gotDir != dir || gotRel != filepath.Join(dir, "marker.txt") {
		t.Errorf("workdir=%q rel=%q", gotDir, gotRel)
	}
	if !r.withSystem || r.gotSystem != "SYS" || r.gotModel != "m" {
		t.Errorf("runner got %+v", r)
	}
	_, id2, _ := runSubagentTask(context.Background(), run, TaskSpec{Task: "x", Dir: dir})
	if id1 == "" || id1 == id2 {
		t.Errorf("ids not unique: %q %q", id1, id2)
	}
}

// #304: the flow role limits in TaskSpec reach the child options.
func TestRunSubagentTask_PassesCompactLimits(t *testing.T) {
	r := &recordingRunner{}
	if _, _, err := runSubagentTask(context.Background(), r, TaskSpec{Task: "x", SoftLimit: 100000, HardLimit: 150000}); err != nil {
		t.Fatal(err)
	}
	if r.gotOpts.SoftLimit != 100000 || r.gotOpts.HardLimit != 150000 {
		t.Fatalf("opts limits = %d, %d", r.gotOpts.SoftLimit, r.gotOpts.HardLimit)
	}
}

func TestRunSubagentTask_ErrorIsReturned(t *testing.T) {
	r := &recordingRunner{returnErr: os.ErrInvalid}
	_, _, err := runSubagentTask(context.Background(), r, TaskSpec{Task: "x"})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("err = %v", err)
	}
}

type ctxRunner struct {
	rec *recordingRunner
	fn  func(context.Context)
}

func (c *ctxRunner) RunTask(ctx context.Context, task, model string, o SubagentOptions) (string, error) {
	c.fn(ctx)
	return c.rec.RunTask(ctx, task, model, o)
}

func (c *ctxRunner) RunTaskWithSystem(ctx context.Context, task, model, sys string, o SubagentOptions) (string, error) {
	c.fn(ctx)
	return c.rec.RunTaskWithSystem(ctx, task, model, sys, o)
}

func TestSubagentTask_NoJSONFieldForWorkdir(t *testing.T) {
	var st subagentTask
	if err := json.Unmarshal([]byte(`{"workdir":"/etc","system_prompt":"x","systemPrompt":"x","dir":"/etc"}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.systemPrompt != "" {
		t.Errorf("systemPrompt set from JSON: %q", st.systemPrompt)
	}
	rt := reflect.TypeOf(st)
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		switch tag {
		case "workdir", "system_prompt", "systemPrompt", "dir":
			t.Errorf("field %s has json tag %q", rt.Field(i).Name, tag)
		}
	}
}

type inlineJobStarter struct{}

func (inlineJobStarter) Start(ctx context.Context, _, _, _ string, fn func(context.Context, string) (string, bool, error)) JobHandle {
	_, _, _ = fn(ctx, "job1")
	return testJobHandle{"job1"}
}

func TestRunSubagentTask_NamedJobIsAskUnroutable(t *testing.T) {
	oldStarter := getJobStarter()
	SetJobStarter(inlineJobStarter{})
	oldInst := subagentToolInstance
	t.Cleanup(func() {
		SetJobStarter(oldStarter)
		subagentToolInstance = oldInst
	})
	var unroutable bool
	var jobID any
	run := &ctxRunner{rec: &recordingRunner{}, fn: func(ctx context.Context) {
		unroutable, _ = ctx.Value(AskUnroutableCtxKey{}).(bool)
		jobID = ctx.Value(JobIDCtxKey{})
	}}
	subagentToolInstance = &SubagentTool{Runner: run}
	if _, _, err := RunSubagentTask(context.Background(), TaskSpec{Task: "x", Name: "run/worker"}); err != nil {
		t.Fatal(err)
	}
	if !unroutable || jobID != "job1" {
		t.Errorf("unroutable=%v jobID=%v", unroutable, jobID)
	}
}

// #340: a named run returns its job id as the session id, so Resume can use it.
func TestRunSubagentTask_NamedRunSessionIsJobID(t *testing.T) {
	oldStarter := getJobStarter()
	SetJobStarter(inlineJobStarter{})
	oldInst := subagentToolInstance
	t.Cleanup(func() {
		SetJobStarter(oldStarter)
		subagentToolInstance = oldInst
	})
	subagentToolInstance = &SubagentTool{Runner: &ctxRunner{rec: &recordingRunner{}, fn: func(context.Context) {}}}
	if _, id, err := RunSubagentTask(context.Background(), TaskSpec{Task: "x", Name: "run/worker"}); err != nil || id != "job1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

type fakeResumer struct {
	jobID, task, dir string
	unroutable       bool
}

func (f *fakeResumer) Resume(ctx context.Context, jobID, task string) (JobHandle, error) {
	f.jobID, f.task, f.dir = jobID, task, Workdir(ctx)
	f.unroutable, _ = ctx.Value(AskUnroutableCtxKey{}).(bool)
	return testJobHandle{"job2"}, nil
}

type fakeWaiter struct{ calls int }

func (w *fakeWaiter) Wait(_ context.Context, id string, _ time.Duration) (JobStatus, bool) {
	w.calls++
	return JobStatus{ID: id, Done: w.calls > 1, Success: true, Content: "written"}, true
}

func TestRunSubagentTask_ResumeSendsTaskToSession(t *testing.T) {
	oldResumer := getJobResumer()
	tool, _ := lookupTool("wait")
	wt := tool.(*WaitTool)
	oldWaiter := wt.waiter()
	t.Cleanup(func() {
		SetJobResumer(oldResumer)
		SetJobWaiter(oldWaiter)
	})
	r, w := &fakeResumer{}, &fakeWaiter{}
	SetJobResumer(r)
	SetJobWaiter(w)
	dir := t.TempDir()
	out, id, err := RunSubagentTask(context.Background(), TaskSpec{Task: "write it", Resume: "job1", Dir: dir})
	if err != nil || out != "written" || id != "job2" {
		t.Fatalf("out=%q id=%q err=%v", out, id, err)
	}
	if r.jobID != "job1" || r.task != "write it" || r.dir != dir || !r.unroutable || w.calls != 2 {
		t.Errorf("resumer=%+v waits=%d", r, w.calls)
	}
}

type recoverJobStarter struct{}

func (recoverJobStarter) Start(ctx context.Context, _, _, _ string, fn func(context.Context, string) (string, bool, error)) JobHandle {
	func() {
		defer func() { _ = recover() }()
		_, _, _ = fn(ctx, "job1")
	}()
	return testJobHandle{"job1"}
}

func TestRunSubagentTask_NamedJobPanicIsError(t *testing.T) {
	oldStarter := getJobStarter()
	SetJobStarter(recoverJobStarter{})
	oldInst := subagentToolInstance
	t.Cleanup(func() {
		SetJobStarter(oldStarter)
		subagentToolInstance = oldInst
	})
	run := &ctxRunner{rec: &recordingRunner{}, fn: func(context.Context) { panic("boom") }}
	subagentToolInstance = &SubagentTool{Runner: run}
	if _, _, err := RunSubagentTask(context.Background(), TaskSpec{Task: "x", Name: "run/worker"}); err == nil {
		t.Fatal("want an error after a panic in the role agent")
	}
}
