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

func init() {
	cronCmd.AddCommand(cronListCmd, cronRunCmd, cronRunNowCmd)
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
