package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci/internal/cron"
)

// CronTool implements the "cron" tool: prompts that run later, on a schedule,
// without anybody being there.
//
// It exists because "check this again in an hour" and "do this every morning"
// are ordinary requests that nothing in a chat session can honour: the session
// ends, and with it every intention it held. A scheduled job is the only kind
// of memory that acts.
//
// The schedule lives in ~/.tyci/cron.json and the runs in ~/.tyci/cron-logs,
// so a job outlives the session that created it. Each run is a fresh one-shot
// agent with only the saved prompt — no history, which is why the prompt has
// to stand on its own.
//
// A project-local <wd>/.tyci/cron.json (TODO.md item 22) unions with the
// global list — SetLocalCronDir below — so a repository can ship its own
// scheduled jobs alongside the machine-wide ones, local winning on a
// same-name collision (internal/cron.LoadMerged/FindJobDir). A scheduled
// job is a whole unattended agent turn, so this is trust-gated exactly like
// hooks.json and .tyci/tools (see commands.go's initCommon).
type CronTool struct{}

func (t *CronTool) Name() string { return "cron" }

// cronConfigDirName is tyci's global state directory, spelled out here rather
// than imported from package agent: tools must not depend on agent, which
// depends on tools.
const cronConfigDirName = ".tyci"

// cronConfigDir is ~/.tyci, where the job list and the logs live. Global
// rather than per-project on purpose: the schedule belongs to the machine, and
// each job records the directory it runs in.
func cronConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cronConfigDirName
	}
	return filepath.Join(home, cronConfigDirName)
}

// localCronDirMu/localCronDir hold the project-local cron directory once
// commands.go's initCommon has decided the project trusted, mirroring
// backgroundBashEnabled and jobStarter's "set once at startup" shape. Empty
// means no project-local override — either untrusted, or no project
// directory was resolved at all.
var (
	localCronDirMu sync.RWMutex
	localCronDir   string
)

// SetLocalCronDir records the project-local .tyci directory (holding
// cron.json) to union with the global schedule. Callers must only pass a
// non-empty dir once the project has been decided trusted — see this
// package's doc comment and commands.go's initCommon, which gates
// hooks.json and .tyci/tools the same way.
func SetLocalCronDir(dir string) {
	localCronDirMu.Lock()
	localCronDir = dir
	localCronDirMu.Unlock()
}

// getLocalCronDir reads the value SetLocalCronDir last recorded.
func getLocalCronDir() string {
	localCronDirMu.RLock()
	defer localCronDirMu.RUnlock()
	return localCronDir
}

// GetLocalCronDirForTests exposes the current value for callers outside
// this package (commands.go's trust-wiring tests) that need to assert what
// initCommon recorded, without a second exported setter's worth of API
// surface for production code to accidentally call.
func GetLocalCronDirForTests() string { return getLocalCronDir() }

// cronDirs returns the ordered list of jobs-file directories to consult:
// just the global one, or [global, project-local] once a trusted project
// directory has been recorded — the order internal/cron.LoadMerged and
// FindJobDir expect (local last, so it wins on a name collision).
func cronDirs() []string {
	if local := getLocalCronDir(); local != "" {
		return []string{cronConfigDir(), local}
	}
	return []string{cronConfigDir()}
}

func (t *CronTool) Run(ctx context.Context, input map[string]any) ToolResult {
	action := strings.TrimSpace(stringParam(input, "action", "list"))
	name := strings.TrimSpace(stringParam(input, "name", ""))

	switch action {
	case "list", "":
		return t.list()
	case "add":
		return t.add(ctx, input, name)
	case "remove", "delete":
		return t.remove(name)
	case "enable":
		return t.setDisabled(name, false)
	case "disable":
		return t.setDisabled(name, true)
	case "logs":
		return t.logs(name)
	case "run_now":
		return t.runNow(ctx, name)
	default:
		return failf("unknown action %q; use \"list\", \"add\", \"remove\", \"enable\", \"disable\", \"logs\" or \"run_now\"", action)
	}
}

func (t *CronTool) list() ToolResult {
	f, err := cron.LoadMerged(cronDirs()...)
	if err != nil {
		return failf("%v", err)
	}
	if len(f.Jobs) == 0 {
		return okf("no scheduled jobs. Add one with cron(action=\"add\", name=\"...\", schedule=\"every 30m\" or \"at 07:30\", prompt=\"...\") when something has to happen later or repeatedly — that is the only way it survives this session.")
	}
	now := time.Now()
	var b strings.Builder
	fmt.Fprintf(&b, "%d scheduled job(s):", len(f.Jobs))
	for _, j := range f.Jobs {
		s, err := j.Parsed()
		if err != nil {
			fmt.Fprintf(&b, "\n- %s: BROKEN schedule %q — it will never run until fixed", j.Name, j.Schedule)
			continue
		}
		state := "next run " + cronWhen(now, s.Next(now, j.LastRun))
		if j.Disabled {
			state = "disabled"
		}
		last := "never run"
		if !j.LastRun.IsZero() {
			last = fmt.Sprintf("last run %s: %s", cronWhen(now, j.LastRun), j.LastStatus)
		}
		fmt.Fprintf(&b, "\n- %s (%s, %s, %s) in %s: %s", j.Name, s, state, last, j.Dir, cronFirstLine(j.Prompt))
	}
	if !CronTickerRunning() {
		b.WriteString("\n\nNothing is running these right now: this session has no scheduler. An interactive tyci session runs the schedule; a one-shot run does not. Say so rather than promising a job will fire.")
	}
	return okf("%s", b.String())
}

func (t *CronTool) add(ctx context.Context, input map[string]any, name string) ToolResult {
	if name == "" {
		return failf("name is required: a short slug like \"nightly-tests\" that identifies the job later and names its log")
	}
	if safe := cron.SanitizeName(name); safe != name {
		return failf("%q cannot be a job name (it names a log file); use letters, digits, - and _ — e.g. %q", name, safe)
	}
	prompt := strings.TrimSpace(stringParam(input, "prompt", ""))
	if prompt == "" {
		return failf("prompt is required: it is what a fresh agent will be asked to do, with NO memory of this conversation, so write it as a standalone instruction — what to do, where, and what counts as done")
	}
	schedule := strings.TrimSpace(stringParam(input, "schedule", ""))
	if _, err := cron.ParseSchedule(schedule); err != nil {
		return failf("%v", err)
	}

	dir := strings.TrimSpace(stringParam(input, "dir", ""))
	if dir == "" {
		dir = Workdir(ctx)
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return failf("dir: %v", err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return failf("dir %s is not a directory; the job needs a project to run in", abs)
	}

	// Checked against the MERGED view (global + project-local, if any) so
	// this cannot add a job whose name collides with one only a
	// project-local cron.json defines — f.Add below only sees the global
	// file, and would otherwise happily create a same-named duplicate that
	// FindJobDir/LoadMerged would then have to arbitrate between.
	if merged, err := cron.LoadMerged(cronDirs()...); err == nil && merged.Find(name) >= 0 {
		return failf("cron: a job named %q already exists (remove it first, or pick another name)", name)
	}

	// New jobs always go into the global ~/.tyci/cron.json — the tool has
	// no notion of "add this to the project's committed file" (a
	// project-local cron.json is meant to be hand-edited and checked into
	// the repo, the same way hooks.json is; nothing writes that one either).
	f, err := cron.Load(cronConfigDir())
	if err != nil {
		return failf("%v", err)
	}
	job := cron.Job{
		Name:     name,
		Prompt:   prompt,
		Dir:      abs,
		Model:    strings.TrimSpace(stringParam(input, "model", "")),
		Schedule: schedule,
	}
	if err := f.Add(job); err != nil {
		return failf("%v", err)
	}
	if err := cron.Save(cronConfigDir(), f); err != nil {
		return failf("%v", err)
	}

	s, _ := job.Parsed()
	msg := fmt.Sprintf("scheduled %q (%s) in %s. A run is a fresh agent with only that prompt — no history from here. Its output goes to %s, readable with cron(action=\"logs\", name=%q).",
		name, s, abs, cron.LogPath(cronConfigDir(), name), name)
	if CronTickerRunning() {
		msg += " This session is running the schedule, and a job that has never run is due at once, so expect it shortly."
	} else {
		msg += " Nothing is running the schedule right now (that only happens in an interactive session), so tell whoever asked that it will fire the next time one is open — do not imply it happens on its own."
	}
	return okf("%s", msg)
}

func (t *CronTool) remove(name string) ToolResult {
	if name == "" {
		return failf("name is required for action=\"remove\"")
	}
	// Operate on whichever dir currently defines the job — the global file
	// for an ordinary job, the project-local cron.json for one that only it
	// defines (FindJobDir prefers the local one on a collision, matching
	// what list/Tick would have used).
	dir, ok := cron.FindJobDir(cronDirs(), name)
	if !ok {
		return failf("no job named %q; call cron(action=\"list\") for the names that exist", name)
	}
	f, err := cron.Load(dir)
	if err != nil {
		return failf("%v", err)
	}
	if !f.Remove(name) {
		return failf("no job named %q; call cron(action=\"list\") for the names that exist", name)
	}
	if err := cron.Save(dir, f); err != nil {
		return failf("%v", err)
	}
	return okf("removed %q. Its log is kept at %s.", name, cron.LogPath(cronConfigDir(), name))
}

func (t *CronTool) setDisabled(name string, disabled bool) ToolResult {
	if name == "" {
		return failf("name is required")
	}
	dir, ok := cron.FindJobDir(cronDirs(), name)
	if !ok {
		return failf("no job named %q; call cron(action=\"list\") for the names that exist", name)
	}
	f, err := cron.Load(dir)
	if err != nil {
		return failf("%v", err)
	}
	i := f.Find(name)
	if i < 0 {
		return failf("no job named %q; call cron(action=\"list\") for the names that exist", name)
	}
	f.Jobs[i].Disabled = disabled
	if err := cron.Save(dir, f); err != nil {
		return failf("%v", err)
	}
	if disabled {
		return okf("%q will not run until it is enabled again. Its prompt and schedule are kept, so this is the reversible way to stop a job.", name)
	}
	return okf("%q is due again.", name)
}

func (t *CronTool) logs(name string) ToolResult {
	if name == "" {
		return failf("name is required for action=\"logs\"")
	}
	path := cron.LogPath(cronConfigDir(), name)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return failf("%q has written no log yet, so it has not run (%s)", name, path)
	}
	if err != nil {
		return failf("%v", err)
	}
	// The tail, not the whole history: a job that has run nightly for a month
	// would otherwise fill the context with runs nobody asked about.
	text := string(data)
	const keep = 8000
	if len(text) > keep {
		text = "…earlier runs omitted…\n" + text[len(text)-keep:]
	}
	return okf("%s", text)
}

func (t *CronTool) runNow(ctx context.Context, name string) ToolResult {
	if name == "" {
		return failf("name is required for action=\"run_now\"")
	}
	f, err := cron.LoadMerged(cronDirs()...)
	if err != nil {
		return failf("%v", err)
	}
	i := f.Find(name)
	if i < 0 {
		return failf("no job named %q; call cron(action=\"list\") for the names that exist", name)
	}
	job := f.Jobs[i]

	runner, err := cronRunner()
	if err != nil {
		return failf("%v", err)
	}
	starter := getJobStarter()
	if starter == nil {
		// No registry: run it inline. Slower for the caller, but a missing
		// registry must not mean a missing feature.
		if err := runner.RunJob(ctx, job); err != nil {
			return failf("%q failed: %v (output: %s)", name, err, cron.LogPath(cronConfigDir(), name))
		}
		return okf("%q finished. Read what it did with cron(action=\"logs\", name=%q).", name, name)
	}

	// A scheduled prompt is a whole agent run, so it goes to the background
	// like any other: you are notified when it finishes and can get on with
	// something else meanwhile.
	bgCtx := context.WithoutCancel(ctx)
	parentID, _ := ctx.Value(JobIDCtxKey{}).(string)
	handle := starter.Start(bgCtx, "cron "+name, JobKindCron, parentID, func(jobCtx context.Context, jobID string) (string, bool, error) {
		err := runner.RunJob(jobCtx, job)
		out := fmt.Sprintf("scheduled job %q finished; output in %s", name, cron.LogPath(cronConfigDir(), name))
		if err != nil {
			return out, false, err
		}
		notifyToParent(parentID, fmt.Sprintf("[scheduled job] %q finished. Read it with cron(action=\"logs\", name=%q).", name, name))
		return out, false, nil
	})
	return okf("running %q now as job %s, out of turn with its schedule. You will be notified when it finishes; read the output with cron(action=\"logs\", name=%q). Do not wait for it unless you have nothing else to do.", name, handle.ID(), name)
}

// cronRunnerExeOverride lets a test point cronRunner() at a fast, harmless
// binary (e.g. "/bin/echo") instead of os.Executable(). Production code
// never sets this (it stays ""); the reason it exists at all is that
// os.Executable() under `go test` IS the compiled test binary, and exec'ing
// that recursively with arbitrary "run"/"--prompt" args is not something a
// test should risk (unpredictable flag parsing, possible recursive test
// execution). Test-only seam, exported to no one outside this package.
var cronRunnerExeOverride string

// cronRunner points at this binary: a scheduled job is the same `tyci run` a
// person would type, so it has to be the same build — unless
// cronRunnerExeOverride says otherwise (tests only, see its doc comment).
func cronRunner() (*cron.Runner, error) {
	exe := cronRunnerExeOverride
	if exe == "" {
		var err error
		exe, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("cannot find the tyci binary to run jobs with: %w", err)
		}
	}
	return &cron.Runner{ConfigDir: cronConfigDir(), LocalDir: getLocalCronDir(), Exe: exe}, nil
}

// ---------------------------------------------------------------------------
// In-session scheduler
// ---------------------------------------------------------------------------

// The interactive modes run the schedule themselves, so a job added mid-session
// actually fires while the session is open instead of waiting for someone to
// start a separate daemon. A one-shot `tyci run` does not: it would start jobs
// and exit before they finished.
var (
	cronTickerMu      sync.Mutex
	cronTickerRunning bool
)

// CronTickerRunning reports whether this process is running the schedule. The
// tool says so in its results: promising that a job will fire when nothing is
// going to run it is the one way this feature can lie.
func CronTickerRunning() bool {
	cronTickerMu.Lock()
	defer cronTickerMu.Unlock()
	return cronTickerRunning
}

// StartCronTicker runs due jobs every interval until ctx is cancelled. Safe to
// call once per process; a second call is a no-op.
func StartCronTicker(ctx context.Context, interval time.Duration) {
	cronTickerMu.Lock()
	if cronTickerRunning {
		cronTickerMu.Unlock()
		return
	}
	cronTickerRunning = true
	cronTickerMu.Unlock()

	if interval <= 0 {
		interval = time.Minute
	}
	runner, err := cronRunner()
	if err == nil {
		runner.OnFinish = cronNotify
	}
	if err != nil {
		cronTickerMu.Lock()
		cronTickerRunning = false
		cronTickerMu.Unlock()
		return
	}

	go func() {
		defer func() {
			cronTickerMu.Lock()
			cronTickerRunning = false
			cronTickerMu.Unlock()
		}()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := runner.Tick(ctx); err != nil {
					// A broken or unreadable jobs file must not kill the
					// scheduler: the next tick may find it fixed.
					continue
				}
			}
		}
	}()
}

// cronNotify tells the session a scheduled job has just run. Without it the
// only record is a log file nobody opens, and a job that fixed (or broke)
// something would go unmentioned.
func cronNotify(j cron.Job, err error) {
	status := "finished"
	if err != nil {
		status = fmt.Sprintf("failed (%v)", err)
	}
	notify(fmt.Sprintf("[scheduled job] %q %s — it ran on its schedule, nobody asked for it just now. Read it with cron(action=\"logs\", name=%q) if it matters to what you are doing; otherwise carry on.", j.Name, status, j.Name))
}

// cronWhen phrases a timestamp relative to now, because "in 20 minutes" is the
// thing the caller needs and a wall-clock time is not.
func cronWhen(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := t.Sub(now)
	switch {
	case d < -time.Minute:
		return fmt.Sprintf("%s ago", d.Round(time.Minute).Abs())
	case d < time.Minute:
		return "now"
	default:
		return "in " + d.Round(time.Minute).String()
	}
}

func cronFirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 120 {
		s = s[:119] + "…"
	}
	return s
}
