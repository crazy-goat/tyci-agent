package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The chat tool lists the workflows of the project that load, and skips the broken one.
func TestChatTools_WorkflowsSkipsBrokenDirectory(t *testing.T) {
	proj := t.TempDir()
	mk := func(name, data string) {
		t.Helper()
		dir := filepath.Join(proj, ".tyci", "workflows", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("issue-to-merge", `{"description":"Merge one issue","start":"end","params":[`+
		`{"name":"issue","description":"GitHub issue number","required":true},`+
		`{"name":"branch","description":"branch to merge into"}],`+
		`"states":{"end":{"end":true}}}`)
	mk("alpha", `{"description":"Other","start":"end","states":{"end":{"end":true}}}`)
	mk("broken", `{"description":`)

	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.m.Info = func() (RepoInfo, error) {
		return RepoInfo{Home: e.home, Root: proj, Repo: "o/r", DefaultBranch: "main", Trusted: true}, nil
	}
	list := ChatTools{e.m}.Workflows()
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "issue-to-merge" {
		t.Fatalf("workflows = %+v", list)
	}
	if list[0].Description != "Other" || len(list[0].Params) != 0 {
		t.Fatalf("alpha = %+v", list[0])
	}
	im := list[1]
	if im.Description != "Merge one issue" || im.Source != filepath.Join(proj, ".tyci", "workflows", "issue-to-merge") {
		t.Fatalf("issue-to-merge = %+v", im)
	}
	if len(im.Params) != 2 || im.Params[0].Name != "issue" || !im.Params[0].Required || im.Params[1].Required {
		t.Fatalf("params = %+v", im.Params)
	}
}

// A missing required param reaches the model with the text that tells it to ask the user.
func TestChatTools_StartMissingParamText(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	_, _, err := ChatTools{e.m}.Start(context.Background(), "demo", nil)
	want := "missing required param issue (GitHub issue number): ask the user, then call workflow_start again"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// A started run shows a chat line with the workflow and its params.
func TestChatTools_StartShowsNotice(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	if _, _, err := (ChatTools{e.m}).Start(context.Background(), "demo", []string{"160"}); err != nil {
		t.Fatal(err)
	}
	if got := e.notice(t); got != "model started /demo 160" {
		t.Fatalf("notice = %q", got)
	}
	close(c.release)
	e.notice(t) // the final notice of the run
}

func TestStartedNotice(t *testing.T) {
	if got := startedNotice("issue-to-merge", []string{"160", "dev"}); got != "model started /issue-to-merge 160 dev" {
		t.Fatalf("notice = %q", got)
	}
	if got := startedNotice("issue-to-merge", nil); got != "model started /issue-to-merge" {
		t.Fatalf("notice without params = %q", got)
	}
}

// A start that fails sends no chat line.
func TestChatTools_StartFailureSendsNoNotice(t *testing.T) {
	e := newMgrEnv(t, &gatedChecks{key: "ok"})
	e.prepErr = errors.New("prepare failed")
	if _, _, err := (ChatTools{e.m}).Start(context.Background(), "demo", []string{"160"}); err == nil {
		t.Fatal("start succeeded")
	}
	select {
	case s := <-e.notices:
		t.Fatalf("unexpected notice %q", s)
	default:
	}
}
