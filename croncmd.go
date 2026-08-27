package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/decodo/tyci/internal/cron"
	"github.com/decodo/tyci/internal/trust"
	"github.com/decodo/tyci/session"
	"github.com/spf13/cobra"
)

var cronCmd = &cobra.Command{
	Use:   "cron",
	Short: "List and manually run scheduled prompts",
}

var cronListCmd = &cobra.Command{
	Use:   "list",
	Short: "List scheduled prompts",
	RunE: func(cmd *cobra.Command, args []string) error {
		f, dirs, err := loadCronFile()
		if err != nil {
			return err
		}
		if len(f.Jobs) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No scheduled jobs.")
			return nil
		}
		now := time.Now()
		for _, j := range f.Jobs {
			status := "enabled"
			if j.Disabled {
				status = "disabled"
			}
			next := "invalid schedule"
			if s, parseErr := j.Parsed(); parseErr == nil {
				next = formatCronTime(s.Next(now, j.LastRun), now)
			}
			last := "never"
			if !j.LastRun.IsZero() {
				last = j.LastRun.Format(time.RFC3339) + " (" + j.LastStatus + ")"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tnext %s\tlast %s\t%s\n", j.Name, status, j.Schedule, next, last, j.Dir)
		}
		_ = dirs // keep the merge source available for future mutating commands.
		return nil
	},
}

var cronRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Run a scheduled prompt immediately",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronJob(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var cronRunNowCmd = &cobra.Command{
	Use:   "run_now <name>",
	Short: "Run a scheduled prompt immediately (alias for run)",
	Args:  cobra.ExactArgs(1),
	RunE:  cronRunCmd.RunE,
}

var cronTickCmd = &cobra.Command{
	Use:   "tick",
	Short: "Run every job that is currently due, then exit",
	Long: `Run every job that is currently due, then exit.

This is the one-shot entry point meant to be invoked by the OS's own
scheduler (cron, launchd, systemd timers, Task Scheduler) rather than by a
person: point it at a schedule of e.g. "every 5m" or "every 1m" and it does
not need a tyci session — interactive, console, or TUI — open anywhere for
jobs to fire. Jobs not yet due are skipped silently; the command always
exits 0 once the check has run, regardless of whether an individual job's
prompt run failed (that failure is recorded in the job's own log and status,
not surfaced as this command's exit code).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCronTick(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	cronCmd.AddCommand(cronListCmd, cronRunCmd, cronRunNowCmd, cronTickCmd)
	rootCmd.AddCommand(cronCmd)
}

func cronConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".tyci"), nil
}

func cronDirs() ([]string, error) {
	global, err := cronConfigDir()
	if err != nil {
		return nil, err
	}
	wd, err := os.Getwd()
	if err != nil {
		return []string{global}, nil
	}
	root, err := session.ProjectKey(wd)
	if err != nil || root == "" {
		return []string{global}, nil
	}
	trusted, _, err := trust.Decide(root, false, nil)
	if err != nil {
		return nil, fmt.Errorf("cron: trust decision: %w", err)
	}
	if !trusted {
		return []string{global}, nil
	}
	// Project-local configuration is rooted at the repository/project root,
	// not the caller's current subdirectory. This keeps `tyci cron` aligned
	// with trust and session project semantics when invoked from repo/subdir.
	return []string{global, filepath.Join(root, ".tyci")}, nil
}

func loadCronFile() (*cron.File, []string, error) {
	dirs, err := cronDirs()
	if err != nil {
		return nil, nil, err
	}
	f, err := cron.LoadMerged(dirs...)
	if err != nil {
		return nil, nil, err
	}
	return f, dirs, nil
}

func runCronJob(ctx context.Context, out interface{ Write([]byte) (int, error) }, name string) error {
	f, dirs, err := loadCronFile()
	if err != nil {
		return err
	}
	i := f.Find(name)
	if i < 0 {
		return fmt.Errorf("cron job %q not found", name)
	}
	configDir, err := cronConfigDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cron: find executable: %w", err)
	}
	runner := &cron.Runner{ConfigDir: configDir, LocalDir: localCronDir(dirs, configDir), Exe: exe, Progress: out}
	if err := runner.RunJob(ctx, f.Jobs[i]); err != nil {
		return fmt.Errorf("cron job %q failed: %w (log: %s)", name, err, cron.LogPath(configDir, name))
	}
	fmt.Fprintf(out, "finished %q (log: %s)\n", name, cron.LogPath(configDir, name))
	return nil
}

// runCronTick is the standalone counterpart to StartCronTicker (tools/cron.go):
// that one runs inside a live tyci session on a minute ticker, this one is a
// single check-and-dispatch meant for the OS's own scheduler to invoke, so
// cron jobs fire whether or not anyone has a tyci session open.
func runCronTick(ctx context.Context, out interface{ Write([]byte) (int, error) }) error {
	_, dirs, err := loadCronFile()
	if err != nil {
		return err
	}
	configDir, err := cronConfigDir()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cron: find executable: %w", err)
	}
	runner := &cron.Runner{ConfigDir: configDir, LocalDir: localCronDir(dirs, configDir), Exe: exe, Progress: out}
	n, err := runner.Tick(ctx)
	if err != nil {
		return fmt.Errorf("cron tick: %w", err)
	}
	if n == 0 {
		fmt.Fprintln(out, "no jobs due")
		return nil
	}
	fmt.Fprintf(out, "ran %d due job(s)\n", n)
	return nil
}

func localCronDir(dirs []string, global string) string {
	if len(dirs) > 1 && dirs[len(dirs)-1] != global {
		return dirs[len(dirs)-1]
	}
	return ""
}

func formatCronTime(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	if t.Equal(now) || t.Before(now) {
		return "now"
	}
	return t.Format(time.RFC3339)
}
