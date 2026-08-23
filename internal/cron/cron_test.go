package cron

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseScheduleAcceptsWhatThePromptsSay(t *testing.T) {
	cases := map[string]string{
		"every 30m":   "every 30m0s",
		"every 6h":    "every 6h0m0s",
		"at 07:30":    "at 07:30",
		"at 22:00":    "at 22:00",
		"@daily 5:05": "at 05:05",
		// Bare forms, because they are what people type by accident.
		"2h":    "every 2h0m0s",
		"07:30": "at 07:30",
		// Case is not meaningful on a command line.
		"EVERY 45M": "every 45m0s",
	}
	for in, want := range cases {
		s, err := ParseSchedule(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if s.String() != want {
			t.Errorf("%q parsed as %q, want %q", in, s.String(), want)
		}
		// String must round-trip, or `cron list` shows something that cannot
		// be typed back in.
		if again, err := ParseSchedule(s.String()); err != nil || again != s {
			t.Errorf("%q does not round-trip through %q: %v", in, s.String(), err)
		}
	}
}

func TestParseScheduleRejectsWhatItCannotHonour(t *testing.T) {
	for _, in := range []string{"", "every 10s", "at 25:00", "at noon", "every tuesday", "* * * * *"} {
		if s, err := ParseSchedule(in); err == nil {
			t.Errorf("%q was accepted as %q; a schedule that silently never fires is worse than an error", in, s)
		}
	}
}

// TestTooOftenSaysWhatTheLimitIs: a rejection the person cannot act on is
// only half a rejection.
func TestTooOftenSaysWhatTheLimitIs(t *testing.T) {
	_, err := ParseSchedule("every 5s")
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if !strings.Contains(err.Error(), "1m") {
		t.Errorf("error should name the shortest interval: %v", err)
	}
}

func TestIntervalIsMeasuredFromTheLastRun(t *testing.T) {
	s, err := ParseSchedule("every 30m")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.Local)

	// Never run: due at once, so a new job can be verified immediately
	// instead of after a full interval.
	if !s.Due(now, time.Time{}) {
		t.Error("a job that has never run should be due")
	}
	if s.Due(now, now.Add(-20*time.Minute)) {
		t.Error("20 minutes after a run is not 30")
	}
	if !s.Due(now, now.Add(-30*time.Minute)) {
		t.Error("exactly one interval later is due")
	}
	if got := s.Next(now, now.Add(-20*time.Minute)); !got.Equal(now.Add(10 * time.Minute)) {
		t.Errorf("next = %v, want 10 minutes out", got)
	}
}

func TestDailyFiresOncePerDay(t *testing.T) {
	s, err := ParseSchedule("at 07:30")
	if err != nil {
		t.Fatal(err)
	}
	day := func(h, m int) time.Time { return time.Date(2026, 8, 22, h, m, 0, 0, time.Local) }

	if s.Due(day(7, 0), day(0, 0).AddDate(0, 0, -1).Add(7*time.Hour+30*time.Minute)) {
		t.Error("before today's slot, with yesterday's run done, is not due")
	}
	if !s.Due(day(7, 30), time.Time{}) {
		t.Error("at the slot, never run, must be due")
	}
	if !s.Due(day(23, 0), day(0, 0).AddDate(0, 0, -1)) {
		t.Error("after the slot, last run yesterday, must be due")
	}
	// The point of the whole thing: it must not fire again the same day.
	if s.Due(day(23, 0), day(7, 30)) {
		t.Error("already ran today at the slot; must not run again")
	}
	if got := s.Next(day(9, 0), day(7, 30)); !got.Equal(day(7, 30).AddDate(0, 0, 1)) {
		t.Errorf("next = %v, want tomorrow's slot", got)
	}
}

func TestStoreRoundTripAndDuplicateNames(t *testing.T) {
	dir := t.TempDir()

	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Jobs) != 0 {
		t.Fatal("a missing file must read as no jobs, not an error")
	}

	if err := f.Add(Job{Name: "nightly", Prompt: "run the tests", Dir: dir, Schedule: "at 02:00"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(Job{Name: "NIGHTLY", Prompt: "again", Dir: dir, Schedule: "at 03:00"}); err == nil {
		t.Error("a second job with the same name (any case) would be indistinguishable in cron list")
	}
	if err := f.Add(Job{Name: "no-prompt", Dir: dir, Schedule: "every 1h"}); err == nil {
		t.Error("a job with no prompt has nothing to run")
	}
	if err := f.Add(Job{Name: "bad-schedule", Prompt: "x", Dir: dir, Schedule: "every tuesday"}); err == nil {
		t.Error("an unparseable schedule must be refused at add time, not silently skipped forever")
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}

	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Jobs) != 1 || back.Jobs[0].Prompt != "run the tests" {
		t.Fatalf("round trip lost the job: %+v", back.Jobs)
	}
	if !back.Remove("nightly") {
		t.Error("remove reported nothing to remove")
	}
	if back.Remove("nightly") {
		t.Error("removing twice reported a success")
	}
}

// TestOneBrokenJobDoesNotStopTheOthers: the jobs file can be hand-edited, and
// a typo in one entry must not silence the rest.
func TestOneBrokenJobDoesNotStopTheOthers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte(`{"jobs":[
	  {"name":"broken","prompt":"x","schedule":"every tuesday"},
	  {"name":"fine","prompt":"y","schedule":"every 1h"},
	  {"name":"off","prompt":"z","schedule":"every 1h","disabled":true}
	]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	due := f.Due(time.Now())
	if len(due) != 1 || due[0].Name != "fine" {
		t.Fatalf("due = %+v, want just the one good enabled job", due)
	}
	if got := f.Broken(); len(got) != 1 || !strings.Contains(got[0].Error(), "broken") {
		t.Fatalf("Broken() = %v, want the bad job named so it can be reported", got)
	}
}

func TestSanitizeNameKeepsLogsInsideTheLogDirectory(t *testing.T) {
	for _, in := range []string{"../../etc/passwd", "a/b", "", "  "} {
		got := SanitizeName(in)
		if strings.ContainsAny(got, `/\`) || got == "" || strings.Contains(got, "..") {
			t.Errorf("SanitizeName(%q) = %q, which can escape the log directory", in, got)
		}
	}
	if got := SanitizeName("nightly-tests_2"); got != "nightly-tests_2" {
		t.Errorf("an ordinary name must survive unchanged, got %q", got)
	}
}

// TestRunJobRecordsTheOutcome drives the real Runner against a script standing
// in for the tyci binary, so the exec path, the log file and the LastRun
// write-back are all exercised.
func TestRunJobRecordsTheOutcome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	work := t.TempDir()
	exe := filepath.Join(dir, "fake-tyci")
	script := "#!/bin/sh\necho \"ran in $(pwd) with: $*\"\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	f, _ := Load(dir)
	job := Job{Name: "probe", Prompt: "look around", Dir: work, Schedule: "every 1h"}
	if err := f.Add(job); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 8, 22, 9, 0, 0, 0, time.Local)
	r := &Runner{ConfigDir: dir, Exe: exe, Now: func() time.Time { return at }}
	if err := r.RunJob(context.Background(), job); err != nil {
		t.Fatalf("RunJob: %v", err)
	}

	logged, err := os.ReadFile(LogPath(dir, "probe"))
	if err != nil {
		t.Fatalf("no log written: %v", err)
	}
	text := string(logged)
	for _, want := range []string{"look around", "--prompt", work} {
		if !strings.Contains(text, want) {
			t.Errorf("log is missing %q:\n%s", want, text)
		}
	}

	back, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Jobs[back.Find("probe")]
	if !got.LastRun.Equal(at) {
		t.Errorf("last run = %v, want %v — without this the job fires again immediately", got.LastRun, at)
	}
	if got.LastStatus != "ok" {
		t.Errorf("status = %q, want ok", got.LastStatus)
	}
	// And now it must no longer be due.
	if len(back.Due(at.Add(time.Minute))) != 0 {
		t.Error("the job is still due a minute after it ran")
	}
}

func TestFailedRunIsRecordedAsFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-tyci")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	job := Job{Name: "fails", Prompt: "x", Dir: dir, Schedule: "every 1h"}
	_ = f.Add(job)
	_ = Save(dir, f)

	r := &Runner{ConfigDir: dir, Exe: exe}
	if err := r.RunJob(context.Background(), job); err == nil {
		t.Fatal("expected the run's failure to be reported to the caller")
	}
	back, _ := Load(dir)
	if got := back.Jobs[back.Find("fails")].LastStatus; !strings.HasPrefix(got, "failed") {
		t.Errorf("status = %q, want a failure", got)
	}
	logged, _ := os.ReadFile(LogPath(dir, "fails"))
	if !strings.Contains(string(logged), "boom") {
		t.Errorf("stderr must reach the log, or a failed run is unreadable: %s", logged)
	}
}

// TestTickRunsOnlyWhatIsDue is the scheduler's whole contract.
func TestTickRunsOnlyWhatIsDue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-tyci")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.Local)

	f, _ := Load(dir)
	_ = f.Add(Job{Name: "due", Prompt: "a", Dir: dir, Schedule: "every 1h"})
	_ = f.Add(Job{Name: "notdue", Prompt: "b", Dir: dir, Schedule: "every 1h"})
	_ = f.Add(Job{Name: "off", Prompt: "c", Dir: dir, Schedule: "every 1h", Disabled: true})
	f.Jobs[f.Find("notdue")].LastRun = now.Add(-10 * time.Minute)
	f.Jobs[f.Find("off")].LastRun = time.Time{}
	if err := Save(dir, f); err != nil {
		t.Fatal(err)
	}

	r := &Runner{ConfigDir: dir, Exe: exe, Now: func() time.Time { return now }}
	n, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ran %d jobs, want just the due one", n)
	}
	if _, err := os.Stat(LogPath(dir, "notdue")); !os.IsNotExist(err) {
		t.Error("a job that was not due produced a log")
	}
	if _, err := os.Stat(LogPath(dir, "off")); !os.IsNotExist(err) {
		t.Error("a disabled job ran")
	}
}

func TestTrimLogKeepsTheNewestPart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.log")
	old := strings.Repeat("old line that nobody will read again\n", 40000)
	if err := os.WriteFile(path, []byte(old+"the newest line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := TrimLog(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxLogBytes {
		t.Errorf("log is still %d bytes", info.Size())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "the newest line") {
		t.Error("trimming dropped the newest line, which is the one that matters")
	}
	if !strings.HasPrefix(string(data), "--- older entries trimmed") {
		t.Error("a trimmed log should say so, or it reads as the whole history")
	}
}

// =============================================================================
// project-local cron.json merge (TODO.md item 22)
// =============================================================================

func TestLoadMerged_UnionsGlobalAndLocal(t *testing.T) {
	global, local := t.TempDir(), t.TempDir()

	gf, _ := Load(global)
	_ = gf.Add(Job{Name: "global-job", Prompt: "a", Dir: global, Schedule: "every 1h"})
	_ = Save(global, gf)

	lf, _ := Load(local)
	_ = lf.Add(Job{Name: "local-job", Prompt: "b", Dir: local, Schedule: "every 1h"})
	_ = Save(local, lf)

	merged, err := LoadMerged(global, local)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2: %+v", len(merged.Jobs), merged.Jobs)
	}
	if merged.Find("global-job") < 0 || merged.Find("local-job") < 0 {
		t.Errorf("expected both jobs present, got %+v", merged.Jobs)
	}
}

// TestLoadMerged_LocalWinsOnNameCollision: a same-named job on both sides —
// the local definition (and its schedule/prompt) must be the one that ends
// up in the merged list, mirroring mcp.json's and skills/'s "local wins".
func TestLoadMerged_LocalWinsOnNameCollision(t *testing.T) {
	global, local := t.TempDir(), t.TempDir()

	gf, _ := Load(global)
	_ = gf.Add(Job{Name: "shared", Prompt: "global prompt", Dir: global, Schedule: "every 1h"})
	_ = Save(global, gf)

	lf, _ := Load(local)
	_ = lf.Add(Job{Name: "shared", Prompt: "local prompt", Dir: local, Schedule: "every 2h"})
	_ = Save(local, lf)

	merged, err := LoadMerged(global, local)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Jobs) != 1 {
		t.Fatalf("got %d jobs, want exactly 1 (local wins): %+v", len(merged.Jobs), merged.Jobs)
	}
	if merged.Jobs[0].Prompt != "local prompt" {
		t.Errorf("Prompt = %q, want the local definition to win", merged.Jobs[0].Prompt)
	}
}

func TestFindJobDir_PrefersLocalOnCollisionElseGlobal(t *testing.T) {
	global, local := t.TempDir(), t.TempDir()

	gf, _ := Load(global)
	_ = gf.Add(Job{Name: "shared", Prompt: "g", Dir: global, Schedule: "every 1h"})
	_ = gf.Add(Job{Name: "global-only", Prompt: "g", Dir: global, Schedule: "every 1h"})
	_ = Save(global, gf)

	lf, _ := Load(local)
	_ = lf.Add(Job{Name: "shared", Prompt: "l", Dir: local, Schedule: "every 1h"})
	_ = Save(local, lf)

	if dir, ok := FindJobDir([]string{global, local}, "shared"); !ok || dir != local {
		t.Errorf("FindJobDir(shared) = %q, %v, want %q, true", dir, ok, local)
	}
	if dir, ok := FindJobDir([]string{global, local}, "global-only"); !ok || dir != global {
		t.Errorf("FindJobDir(global-only) = %q, %v, want %q, true", dir, ok, global)
	}
	if _, ok := FindJobDir([]string{global, local}, "nowhere"); ok {
		t.Error("FindJobDir found a dir for a job that exists in neither")
	}
}

// TestRunnerTick_RunsAProjectLocalJobAndMarksItInTheLocalFile is the
// end-to-end guarantee: with LocalDir set, Tick must pick up a job that only
// the project-local cron.json defines, run it, and write LastRun/LastStatus
// back into the SAME (local) file rather than silently dropping it or
// writing a stray entry into the global one.
func TestRunnerTick_RunsAProjectLocalJobAndMarksItInTheLocalFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	global, local := t.TempDir(), t.TempDir()
	exe := filepath.Join(global, "fake-tyci")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.Local)

	lf, _ := Load(local)
	_ = lf.Add(Job{Name: "local-only", Prompt: "x", Dir: local, Schedule: "every 1h"})
	if err := Save(local, lf); err != nil {
		t.Fatal(err)
	}

	r := &Runner{ConfigDir: global, LocalDir: local, Exe: exe, Now: func() time.Time { return now }}
	n, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ran %d jobs, want 1", n)
	}

	// The run-state write-back landed in the local file...
	backLocal, _ := Load(local)
	got := backLocal.Jobs[backLocal.Find("local-only")]
	if !got.LastRun.Equal(now) {
		t.Errorf("local file's LastRun = %v, want %v", got.LastRun, now)
	}
	if got.LastStatus != "ok" {
		t.Errorf("local file's LastStatus = %q, want ok", got.LastStatus)
	}
	// ...and did NOT spawn a stray entry in the global file.
	backGlobal, _ := Load(global)
	if len(backGlobal.Jobs) != 0 {
		t.Errorf("expected no jobs written into the global file, got %+v", backGlobal.Jobs)
	}
}

// TestRunnerTick_WithoutLocalDir_IgnoresLocalFile is the trust-gate
// guarantee at the Runner level: LocalDir=="" (the untrusted-project
// default) must behave exactly like Load(ConfigDir) always has, never
// looking at a project-local cron.json even if one exists on disk.
func TestRunnerTick_WithoutLocalDir_IgnoresLocalFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	global, local := t.TempDir(), t.TempDir()
	exe := filepath.Join(global, "fake-tyci")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.Local)

	lf, _ := Load(local)
	_ = lf.Add(Job{Name: "local-only", Prompt: "x", Dir: local, Schedule: "every 1h"})
	if err := Save(local, lf); err != nil {
		t.Fatal(err)
	}

	r := &Runner{ConfigDir: global, Exe: exe, Now: func() time.Time { return now }} // LocalDir unset
	n, err := r.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("ran %d jobs, want 0 — LocalDir is unset, so the local file must not be consulted", n)
	}
}
