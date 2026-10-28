// Package daemon is the headless tyci daemon (#215). Pollers read the forge
// and the cron jobs file at an interval and give triggers. A dispatcher starts
// one workflow run per trigger, with a limit of parallel runs. A unix socket
// gives the state of the daemon and its runs over JSON-RPC.
//
// The daemon keeps no database. A trigger is a duplicate when a run file
// under ~/.tyci/runs shows it (see pollers.go), so the dedup survives a restart.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/cron"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/forge"
)

// Trigger kinds.
const (
	KindIssue     = "issue"
	KindCIFailure = "ci_failure"
	KindCron      = "cron"
	// KindResume continues a run that an earlier daemon left running.
	KindResume = "resume"
)

// Trigger asks the daemon to start one run.
type Trigger struct {
	Kind     string
	Repo     string // "owner/name"
	Workflow string
	Issue    int    // KindIssue, and KindResume of an issue run
	Run      string // KindResume: the run to continue
	// Key is the dedup key. The daemon starts one run per key at a time.
	Key string
}

// Poller gives the triggers that are due now.
type Poller interface {
	Poll(ctx context.Context) ([]Trigger, error)
}

// Result is the end of a run, or its pause.
type Result struct {
	Status string // done, failed, stopped or paused
	Reason string
	PR     int
}

// Runs starts the workflow runs of one repository (production: ManagerRuns).
type Runs interface {
	// Start starts the run of t. done receives one Result when the run ends or
	// pauses. An error wrapping flow.ErrBusy means another run has the issue.
	Start(ctx context.Context, t Trigger) (id string, done <-chan Result, err error)
	// Interrupted returns the runs that a stopped tyci left running, so the
	// daemon continues them (KindResume triggers).
	Interrupted() ([]Trigger, error)
}

// Repo is one repository that the issue and ci_failure triggers watch.
type Repo struct {
	Name          string      // "owner/name"
	Forge         forge.Forge // milestones, issues and write access
	CI            forge.CIReader
	Comments      forge.Commenter // nil: the daemon only logs
	Runs          Runs
	AcceptedLabel string
	Workflow      string // the workflow of an issue run
	DefaultBranch string
	CIWorkflow    string // the CI workflow file, for example tests.yaml
}

// Config is the daemon setup.
type Config struct {
	Home         string // run files are under <Home>/.tyci/runs
	Dir          string // holds daemon.sock and daemon.lock; mode 0700
	PollInterval time.Duration
	Workers      int // 0 = unlimited
	Issue        bool
	CIFailure    bool
	// Cron runs one cron tick. Nil: no cron trigger.
	Cron         func(ctx context.Context) error
	CronInterval time.Duration // default 1 minute
	Repos        []Repo
	Log          io.Writer
	// ShutdownWait is how long Run waits for the runs after ctx ends. Runs
	// that are still running stay running and the next start continues them.
	ShutdownWait time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// SocketPath returns the socket of a daemon dir.
func SocketPath(dir string) string { return filepath.Join(dir, "daemon.sock") }

// DefaultDir returns ~/.tyci/daemon.
func DefaultDir(home string) string { return filepath.Join(home, ".tyci", "daemon") }

// Daemon is one daemon process.
type Daemon struct {
	cfg     Config
	started time.Time
	sem     chan struct{}

	logMu sync.Mutex

	mu      sync.Mutex
	pending map[string]bool    // dedup keys from enqueue to the run end
	active  map[string]Trigger // run id -> trigger
	queued  int
	polls   map[string]PollStatus
}

// New returns a daemon. Call Run to start it.
func New(cfg Config) *Daemon {
	if cfg.CronInterval <= 0 {
		cfg.CronInterval = time.Minute
	}
	if cfg.ShutdownWait <= 0 {
		cfg.ShutdownWait = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	d := &Daemon{cfg: cfg, pending: map[string]bool{}, active: map[string]Trigger{}, polls: map[string]PollStatus{}}
	if cfg.Workers > 0 {
		d.sem = make(chan struct{}, cfg.Workers)
	}
	return d
}

// Logf writes one line to the daemon log.
func (d *Daemon) Logf(format string, args ...any) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	fmt.Fprintf(d.cfg.Log, "%s %s\n", d.cfg.Now().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// ErrRunning means another daemon holds the lock.
var ErrRunning = errors.New("daemon already running")

// Run takes the lock, opens the socket, continues the interrupted runs and
// polls until ctx ends. A second daemon gets ErrRunning.
func (d *Daemon) Run(ctx context.Context) error {
	if err := os.MkdirAll(d.cfg.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(d.cfg.Dir, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(d.cfg.Dir, "daemon.lock")
	release, ok, err := cron.TryLock(lockPath)
	if err != nil {
		return err
	}
	if !ok {
		pid, _ := os.ReadFile(lockPath)
		return fmt.Errorf("%w (pid %s)", ErrRunning, strings.TrimSpace(string(pid)))
	}
	defer release()
	// The pid is for the refused second daemon. flock is on the inode, so the
	// rewrite keeps the lock.
	if err := os.WriteFile(lockPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return err
	}
	d.started = d.cfg.Now()

	sock := SocketPath(d.cfg.Dir)
	_ = os.Remove(sock) // a socket that a dead daemon left; the lock says no daemon runs
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(sock) }()
	defer func() { _ = ln.Close() }()
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	go Serve(ln, d, d.Logf)

	// runCtx outlives ctx by ShutdownWait: the handlers watch their runs until then.
	runCtx, stopRuns := context.WithCancel(context.WithoutCancel(ctx))
	defer stopRuns()
	var wg sync.WaitGroup
	for _, r := range d.cfg.Repos {
		ts, err := r.Runs.Interrupted()
		if err != nil {
			d.Logf("%s: cannot read the interrupted runs: %v", r.Name, err)
		}
		for _, t := range ts {
			d.Logf("%s: continue run %s", r.Name, flow.ShowRunID(t.Run))
			d.enqueue(ctx, runCtx, &wg, t)
		}
	}
	var pw sync.WaitGroup
	for name, p := range d.pollers() {
		interval := d.cfg.PollInterval
		if name == KindCron {
			interval = d.cfg.CronInterval
		}
		pw.Add(1)
		go func() {
			defer pw.Done()
			d.pollLoop(ctx, runCtx, &wg, name, p, interval)
		}()
	}
	d.Logf("daemon started: pid %d, %d repo(s), socket %s", os.Getpid(), len(d.cfg.Repos), sock)
	<-ctx.Done()
	pw.Wait()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d.cfg.ShutdownWait):
		d.Logf("daemon stopped with runs still running; the next start continues them")
	}
	stopRuns()
	return nil
}

// pollers returns the enabled pollers by name.
func (d *Daemon) pollers() map[string]Poller {
	ps := map[string]Poller{}
	for _, r := range d.cfg.Repos {
		if d.cfg.Issue {
			ps[KindIssue+" "+r.Name] = &IssuePoller{Repo: r, Home: d.cfg.Home}
		}
		if d.cfg.CIFailure && r.CI != nil {
			ps[KindCIFailure+" "+r.Name] = &CIPoller{Repo: r, Home: d.cfg.Home, Now: d.cfg.Now, Logf: d.Logf}
		}
	}
	if d.cfg.Cron != nil {
		ps[KindCron] = cronPoller(d.cfg.Cron)
	}
	return ps
}

// pollLoop polls now and then every interval. A poll error is logged and the
// next interval tries again.
func (d *Daemon) pollLoop(ctx, runCtx context.Context, wg *sync.WaitGroup, name string, p Poller, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		ts, err := p.Poll(ctx)
		st := PollStatus{Name: name, Last: d.cfg.Now()}
		if err != nil && ctx.Err() == nil {
			st.Error = err.Error()
			d.Logf("%s: poll failed, next try in %s: %v", name, interval, err)
		}
		d.mu.Lock()
		d.polls[name] = st
		d.mu.Unlock()
		for _, tr := range ts {
			d.enqueue(ctx, runCtx, wg, tr)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// enqueue starts a goroutine that waits for a worker slot and starts the run.
// A trigger whose key is pending is dropped.
func (d *Daemon) enqueue(ctx, runCtx context.Context, wg *sync.WaitGroup, t Trigger) {
	d.mu.Lock()
	if d.pending[t.Key] {
		d.mu.Unlock()
		return
	}
	d.pending[t.Key] = true
	d.queued++
	d.mu.Unlock()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.handle(ctx, runCtx, t)
	}()
}

func (d *Daemon) repo(name string) (Repo, bool) {
	for _, r := range d.cfg.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return Repo{}, false
}

// handle runs one trigger: it waits for a slot, starts the run and watches it
// until it ends or pauses. A paused or failed run gets one issue comment.
func (d *Daemon) handle(ctx, runCtx context.Context, t Trigger) {
	release := func() {}
	if d.sem != nil {
		select {
		case d.sem <- struct{}{}:
			release = func() { <-d.sem }
		case <-ctx.Done():
			d.mu.Lock()
			d.queued--
			delete(d.pending, t.Key)
			d.mu.Unlock()
			return
		}
	}
	defer release()
	d.mu.Lock()
	d.queued--
	d.mu.Unlock()
	r, ok := d.repo(t.Repo)
	if !ok {
		d.Logf("%s: no such repo %s", t.Key, t.Repo)
		return
	}
	id, done, err := r.Runs.Start(runCtx, t)
	if errors.Is(err, flow.ErrBusy) {
		d.Logf("%s: not started, another run has it: %v", t.Key, err)
		d.forget(t.Key, "")
		return
	}
	if err != nil {
		// The key stays pending: this daemon does not try again. A restart does.
		d.Logf("%s: start failed: %v", t.Key, err)
		d.comment(runCtx, r, t.Issue, fmt.Sprintf("tyci daemon could not start workflow `%s` for this issue: %s", t.Workflow, err))
		return
	}
	d.Logf("%s: run %s started (workflow %s)", t.Key, flow.ShowRunID(id), t.Workflow)
	d.mu.Lock()
	d.active[id] = t
	d.mu.Unlock()
	defer d.forget(t.Key, id)
	var res Result
	select {
	case res = <-done:
	case <-runCtx.Done():
		return
	}
	show := flow.ShowRunID(id)
	switch res.Status {
	case "paused":
		d.Logf("%s: run %s paused, it waits for a human", t.Key, show)
		d.comment(runCtx, r, t.Issue, fmt.Sprintf("tyci run %s waits for a human: %s\n\nContinue it with `tyci workflow resume %s <answer>` (see `tyci workflow status %s`).", show, res.Reason, id, id))
	case "failed":
		d.Logf("%s: run %s failed: %s", t.Key, show, res.Reason)
		d.comment(runCtx, r, t.Issue, fmt.Sprintf("tyci run %s failed: %s\n\nSee `tyci workflow status %s`. The daemon does not start it again.", show, res.Reason, id))
	default:
		d.Logf("%s: run %s %s", t.Key, show, res.Status)
	}
}

// forget ends the dedup of key in memory; the run file keeps it.
func (d *Daemon) forget(key, id string) {
	d.mu.Lock()
	delete(d.pending, key)
	delete(d.active, id)
	d.mu.Unlock()
}

// comment posts body, with the secrets masked, on issue, or only logs when that is not possible.
func (d *Daemon) comment(ctx context.Context, r Repo, issue int, body string) {
	if issue <= 0 || r.Comments == nil {
		return
	}
	// The run texts can hold a token (an error with a push URL); the issue can
	// be public.
	if err := r.Comments.Comment(ctx, issue, flow.MaskSecrets(body)); err != nil {
		d.Logf("%s: comment on #%d failed: %v", r.Name, issue, err)
	}
}
