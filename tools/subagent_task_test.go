package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
