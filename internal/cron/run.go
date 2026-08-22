package cron

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Runner executes due jobs. Each run is a separate `tyci run --prompt`
// process rather than an in-process agent: a scheduled prompt is exactly the
// one-shot invocation that command already is, and a crash or a wedged tool
// then takes down one run instead of the scheduler.
type Runner struct {
	// ConfigDir is the ~/.tyci directory holding the jobs file and the logs.
	ConfigDir string
	// Exe is the tyci binary to invoke. Injected so a test can point it at a
	// script instead.
	Exe string
	// Now defaults to time.Now. Injected so schedule behaviour can be tested
	// without waiting for a clock.
	Now func() time.Time
	// Progress receives one line per started and finished run, for whoever is
	// watching. nil discards them; the per-job log file is the durable record
	// either way.
	Progress io.Writer
	// OnFinish, when set, is called after every run Tick starts. It is how a
	// session learns that a job it did not ask for has just run — the log
	// alone is something nobody opens.
	OnFinish func(j Job, err error)

	// running guards against a job overlapping itself: a prompt that takes
	// 40 minutes on an "every 30m" schedule must not accumulate copies.
	mu      sync.Mutex
	running map[string]bool
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) logf(format string, args ...any) {
	if r.Progress == nil {
		return
	}
	fmt.Fprintf(r.Progress, format+"\n", args...)
}

// claim marks a job as running, or reports that it already is.
func (r *Runner) claim(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[name] {
		return false
	}
	if r.running == nil {
		r.running = map[string]bool{}
	}
	r.running[name] = true
	return true
}

func (r *Runner) release(name string) {
	r.mu.Lock()
	delete(r.running, name)
	r.mu.Unlock()
}

// RunJob runs one job now, regardless of its schedule, and records the
// outcome. Used by Tick and by `tyci cron run <name>` for a person who wants
// to see whether a job works before waiting for its slot.
func (r *Runner) RunJob(ctx context.Context, j Job) error {
	if !r.claim(j.Name) {
		r.logf("skipped %s: the previous run has not finished", j.Name)
		return nil
	}
	defer r.release(j.Name)

	logPath := LogPath(r.ConfigDir, j.Name)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	_ = TrimLog(logPath)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("cron: open log: %w", err)
	}
	defer f.Close()

	start := r.now()
	fmt.Fprintf(f, "\n=== %s: %s\n", start.Format(time.RFC3339), j.Prompt)
	r.logf("running %s", j.Name)

	args := []string{"run", "--prompt", j.Prompt}
	if j.Model != "" {
		args = append(args, "--model", j.Model)
	}
	cmd := exec.CommandContext(ctx, r.Exe, args...)
	cmd.Dir = j.Dir
	cmd.Stdout = f
	cmd.Stderr = f
	runErr := cmd.Run()

	status := "ok"
	if runErr != nil {
		status = "failed: " + runErr.Error()
	}
	end := r.now()
	fmt.Fprintf(f, "=== %s after %s: %s\n", end.Format(time.RFC3339), end.Sub(start).Round(time.Second), status)
	r.logf("finished %s in %s: %s", j.Name, end.Sub(start).Round(time.Second), status)

	// The finish time, not the start: an "every 30m" job means half an hour
	// between runs, not half an hour between the starts of runs that take
	// longer than that.
	if err := MarkRun(r.ConfigDir, j.Name, end, status); err != nil {
		return err
	}
	return runErr
}

// Tick runs everything due once and returns how many jobs it started.
func (r *Runner) Tick(ctx context.Context) (int, error) {
	f, err := Load(r.ConfigDir)
	if err != nil {
		return 0, err
	}
	for _, bad := range f.Broken() {
		r.logf("skipping: %v", bad)
	}
	due := f.Due(r.now())
	if len(due) == 0 {
		return 0, nil
	}

	// Concurrently, because two jobs due in the same minute should not queue
	// behind each other — each writes only to its own log, and MarkRun
	// rewrites the jobs file from a fresh load.
	var wg sync.WaitGroup
	for _, j := range due {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			err := r.RunJob(ctx, j)
			if err != nil {
				r.logf("%s: %v", j.Name, err)
			}
			if r.OnFinish != nil {
				r.OnFinish(j, err)
			}
		}(j)
	}
	wg.Wait()
	return len(due), nil
}
