package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/internal/cron"
)

// withCronHome points the tool's job list at a temp directory: the tool reads
// ~/.tyci, so without this the tests would edit the developer's real jobs.
func withCronHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func runCron(t *testing.T, input map[string]any) ToolResult {
	t.Helper()
	return (&CronTool{}).Run(context.Background(), input)
}

func TestCronAddThenList(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()

	res := (&CronTool{}).Run(WithWorkdir(context.Background(), wd), map[string]any{
		"action":   "add",
		"name":     "nightly-tests",
		"prompt":   "run the test suite and summarise the failures",
		"schedule": "at 02:00",
	})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	// The prompt runs with no history, and a caller that does not know that
	// writes a prompt referring to "the file we discussed".
	if !strings.Contains(res.Content, "no history") {
		t.Errorf("add should say the run gets no conversation history: %q", res.Content)
	}
	// Nothing ticks in a test process, and saying "it will run" would be a lie.
	if !strings.Contains(res.Content, "Nothing is running the schedule") {
		t.Errorf("add should admit nothing will run it here: %q", res.Content)
	}

	list := runCron(t, map[string]any{"action": "list"})
	if !list.Success {
		t.Fatalf("list failed: %s", list.Error)
	}
	for _, want := range []string{"nightly-tests", "at 02:00", "never run", wd} {
		if !strings.Contains(list.Content, want) {
			t.Errorf("list is missing %q: %q", want, list.Content)
		}
	}
}

// TestCronAddRecordsTheDirectoryItWasAskedIn: the directory decides which
// project the prompt is about, so it is saved now rather than resolved when the
// job fires somewhere else.
func TestCronAddRecordsTheDirectoryItWasAskedIn(t *testing.T) {
	home := withCronHome(t)
	wd := t.TempDir()

	res := (&CronTool{}).Run(WithWorkdir(context.Background(), wd), map[string]any{
		"action": "add", "name": "check", "prompt": "look", "schedule": "every 1h",
	})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	f, err := cron.Load(home + "/.tyci")
	if err != nil {
		t.Fatal(err)
	}
	i := f.Find("check")
	if i < 0 {
		t.Fatal("job not saved")
	}
	if f.Jobs[i].Dir != wd {
		t.Errorf("dir = %q, want the working directory it was added in (%q)", f.Jobs[i].Dir, wd)
	}
}

func TestCronRefusesWhatItCannotHonour(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	base := map[string]any{"action": "add", "name": "x", "prompt": "p", "schedule": "every 1h", "dir": wd}

	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for key, val := range base {
			m[key] = val
		}
		m[k] = v
		return m
	}

	cases := map[string]map[string]any{
		"no name":           with("name", ""),
		"no prompt":         with("prompt", "  "),
		"unusable schedule": with("schedule", "every tuesday"),
		"too often":         with("schedule", "every 5s"),
		"name with a slash": with("name", "../escape"),
		"missing directory": with("dir", wd+"/nope"),
	}
	for label, input := range cases {
		if res := runCron(t, input); res.Success {
			t.Errorf("%s was accepted: %q", label, res.Content)
		}
	}
}

func TestCronDuplicateNameIsRefused(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	input := map[string]any{"action": "add", "name": "dup", "prompt": "p", "schedule": "every 1h", "dir": wd}
	if res := runCron(t, input); !res.Success {
		t.Fatalf("first add failed: %s", res.Error)
	}
	res := runCron(t, input)
	if res.Success {
		t.Fatal("a second job with the same name would be indistinguishable in a listing")
	}
	if !strings.Contains(res.Error, "already exists") {
		t.Errorf("error should say why: %q", res.Error)
	}
}

func TestCronDisableEnableRemove(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	if res := runCron(t, map[string]any{"action": "add", "name": "j", "prompt": "p", "schedule": "every 1h", "dir": wd}); !res.Success {
		t.Fatal(res.Error)
	}

	if res := runCron(t, map[string]any{"action": "disable", "name": "j"}); !res.Success {
		t.Fatal(res.Error)
	}
	if list := runCron(t, map[string]any{"action": "list"}); !strings.Contains(list.Content, "disabled") {
		t.Errorf("list should show the job as disabled: %q", list.Content)
	}
	if res := runCron(t, map[string]any{"action": "enable", "name": "j"}); !res.Success {
		t.Fatal(res.Error)
	}
	if res := runCron(t, map[string]any{"action": "remove", "name": "j"}); !res.Success {
		t.Fatal(res.Error)
	}
	if list := runCron(t, map[string]any{"action": "list"}); !strings.Contains(list.Content, "no scheduled jobs") {
		t.Errorf("the job survived removal: %q", list.Content)
	}
}

// TestCronActionsOnAnUnknownJobSayWhatToDo: a bare "not found" leaves the
// caller guessing whether the name or the whole feature is wrong.
func TestCronActionsOnAnUnknownJobSayWhatToDo(t *testing.T) {
	withCronHome(t)
	for _, action := range []string{"remove", "enable", "disable", "run_now"} {
		res := runCron(t, map[string]any{"action": action, "name": "ghost"})
		if res.Success {
			t.Errorf("%s on a missing job reported success", action)
			continue
		}
		if !strings.Contains(res.Error, "list") {
			t.Errorf("%s error should point at the listing: %q", action, res.Error)
		}
	}
}

func TestCronLogsSaysWhenTheJobHasNeverRun(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	if res := runCron(t, map[string]any{"action": "add", "name": "quiet", "prompt": "p", "schedule": "every 1h", "dir": wd}); !res.Success {
		t.Fatal(res.Error)
	}
	res := runCron(t, map[string]any{"action": "logs", "name": "quiet"})
	if res.Success {
		t.Fatalf("expected a failure explaining there is nothing to read, got %q", res.Content)
	}
	if !strings.Contains(res.Error, "has not run") {
		t.Errorf("error should distinguish 'never ran' from 'no such job': %q", res.Error)
	}
}

func TestCronLogsReturnsTheTail(t *testing.T) {
	home := withCronHome(t)
	wd := t.TempDir()
	if res := runCron(t, map[string]any{"action": "add", "name": "chatty", "prompt": "p", "schedule": "every 1h", "dir": wd}); !res.Success {
		t.Fatal(res.Error)
	}
	path := cron.LogPath(home+"/.tyci", "chatty")
	if err := os.MkdirAll(strings.TrimSuffix(path, "/chatty.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("an old run nobody asked about\n", 2000) + "THE LAST RUN\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCron(t, map[string]any{"action": "logs", "name": "chatty"})
	if !res.Success {
		t.Fatal(res.Error)
	}
	if !strings.Contains(res.Content, "THE LAST RUN") {
		t.Error("the newest run must be in the tail")
	}
	if len(res.Content) > 9000 {
		t.Errorf("logs returned %d bytes; a month of nightly runs would bury the conversation", len(res.Content))
	}
}

func TestCronUnknownActionListsTheRealOnes(t *testing.T) {
	withCronHome(t)
	res := runCron(t, map[string]any{"action": "schedule"})
	if res.Success {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"add", "logs", "run_now"} {
		if !strings.Contains(res.Error, want) {
			t.Errorf("error should list %q as an option: %q", want, res.Error)
		}
	}
}

// =============================================================================
// project-local cron.json (TODO.md item 22)
// =============================================================================

// withLocalCronDir sets and restores SetLocalCronDir for one test, so a
// test that needs a trusted project's cron dir cannot leak it into the
// next one — mirrors withCronHome's own isolation for HOME.
func withLocalCronDir(t *testing.T, dir string) {
	t.Helper()
	SetLocalCronDir(dir)
	t.Cleanup(func() { SetLocalCronDir("") })
}

// writeLocalCronJob writes <wd>/.tyci/cron.json with one job, mirroring
// what a hand-edited, checked-in project cron.json would look like.
func writeLocalCronJob(t *testing.T, wd string, job cron.Job) {
	t.Helper()
	dir := filepath.Join(wd, ".tyci")
	f := &cron.File{}
	if err := f.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := cron.Save(dir, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

func TestCronList_WithoutLocalDir_OnlyShowsGlobalJobs(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	writeLocalCronJob(t, wd, cron.Job{Name: "local-only", Prompt: "p", Dir: wd, Schedule: "every 1h"})
	// SetLocalCronDir deliberately not called: an untrusted (or non-git)
	// project must not have its .tyci/cron.json read at all.

	res := runCron(t, map[string]any{"action": "list"})
	if !res.Success {
		t.Fatal(res.Error)
	}
	if strings.Contains(res.Content, "local-only") {
		t.Error("a project-local job appeared without SetLocalCronDir ever being called — the untrusted default must not read it")
	}
}

func TestCronList_WithLocalDir_UnionsGlobalAndLocalJobs(t *testing.T) {
	home := withCronHome(t)
	wd := t.TempDir()
	writeLocalCronJob(t, wd, cron.Job{Name: "local-only", Prompt: "p", Dir: wd, Schedule: "every 1h"})
	withLocalCronDir(t, filepath.Join(wd, ".tyci"))

	if res := runCron(t, map[string]any{"action": "add", "name": "global-only", "prompt": "p", "schedule": "every 1h", "dir": home}); !res.Success {
		t.Fatal(res.Error)
	}

	res := runCron(t, map[string]any{"action": "list"})
	if !res.Success {
		t.Fatal(res.Error)
	}
	for _, want := range []string{"local-only", "global-only"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("list is missing %q: %q", want, res.Content)
		}
	}
}

// TestCronAdd_RefusesNameCollisionWithLocalOnlyJob: `add` must check the
// merged view before writing to the global file, or a same-named
// project-local job would be silently shadowed (or shadow the new one) the
// next time list/Tick reads them.
func TestCronAdd_RefusesNameCollisionWithLocalOnlyJob(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	writeLocalCronJob(t, wd, cron.Job{Name: "shared-name", Prompt: "local", Dir: wd, Schedule: "every 1h"})
	withLocalCronDir(t, filepath.Join(wd, ".tyci"))

	res := runCron(t, map[string]any{"action": "add", "name": "shared-name", "prompt": "global", "schedule": "every 1h", "dir": wd})
	if res.Success {
		t.Fatal("expected add to refuse a name that collides with a project-local job")
	}
}

// TestCronDisable_ActsOnTheProjectLocalJob checks that an action naming a
// job which only the project-local file defines writes into THAT file, not
// into (or instead of) the global one.
func TestCronDisable_ActsOnTheProjectLocalJob(t *testing.T) {
	withCronHome(t)
	wd := t.TempDir()
	localDir := filepath.Join(wd, ".tyci")
	writeLocalCronJob(t, wd, cron.Job{Name: "local-only", Prompt: "p", Dir: wd, Schedule: "every 1h"})
	withLocalCronDir(t, localDir)

	res := runCron(t, map[string]any{"action": "disable", "name": "local-only"})
	if !res.Success {
		t.Fatal(res.Error)
	}

	f, err := cron.Load(localDir)
	if err != nil {
		t.Fatal(err)
	}
	i := f.Find("local-only")
	if i < 0 || !f.Jobs[i].Disabled {
		t.Errorf("expected local-only to be disabled in the project-local file, got %+v", f.Jobs)
	}
}
