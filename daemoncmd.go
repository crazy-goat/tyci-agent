package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
	"github.com/crazy-goat/tyci-agent/internal/forge"
	"github.com/crazy-goat/tyci-agent/internal/tyciconfig"
)

var daemonDir string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run tyci without a chat: start runs for accepted issues, a red default branch and cron jobs",
	Long: `Run tyci in the foreground without a chat. The daemon polls and starts workflow runs:

  issue       an open issue of the lowest vX.Y.Z milestone with the accepted label and an
              author with write access starts the issue workflow (default issue-to-merge)
  ci_failure  a red newest CI run (TYCI_CI_WORKFLOW, default tests.yaml) on the default
              branch starts the fix-ci workflow
  cron        the due jobs of ~/.tyci/cron.json and the project cron.json run every minute

The issue and ci_failure triggers watch the repositories in daemon.repos of
~/.tyci/config.json. The repository of --dir (default: the current directory) needs
only its name there. Every other repository also needs repos.<owner/name>.path, the
absolute path of its local clone. The daemon logs and skips an entry that does not
open. Without repos the daemon runs cron only.
A run that stops at an ask state stays paused. The daemon posts one issue comment, and a
human continues the run with 'tyci workflow resume <run-id> <answer>'.

Only one daemon runs per user (lock ~/.tyci/daemon/daemon.lock). The daemon answers
JSON-RPC 2.0 on the unix socket ~/.tyci/daemon/daemon.sock (methods status, runs.list,
runs.get). Ctrl+C or SIGTERM stops polling. Runs that do not end within 30 seconds stay
running and the next daemon start continues them.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runDaemon(ctx, cmd, cmd.ErrOrStderr())
	},
}

// runDaemon reads the config, wires the repository of --dir and runs the
// daemon until ctx ends.
func runDaemon(ctx context.Context, cmd *cobra.Command, log io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	info, repoErr := workflowRepoInfo(daemonDir)
	projectCfg := ""
	if repoErr == nil && info.Trusted {
		projectCfg = filepath.Join(info.Root, ".tyci", "config.json")
	}
	dc, err := tyciconfig.LoadDaemon(filepath.Join(home, ".tyci", "config.json"), projectCfg)
	if err != nil {
		return err
	}
	if !dc.Enabled {
		return errors.New("daemon.enabled is false in the config; set it to true to run the daemon")
	}
	var d *daemon.Daemon
	logf := func(format string, args ...any) { d.Logf(format, args...) }
	cfg := daemon.Config{
		Home: home, Dir: daemon.DefaultDir(home), PollInterval: dc.PollInterval, Workers: dc.Workers,
		Issue: dc.Issue, CIFailure: dc.CIFailure, Log: log,
	}
	if dc.Cron {
		cfg.Cron = func(ctx context.Context) error { return runCronTick(ctx, cronLog(logf), daemonDir) }
	}
	// The repository of --dir needs no registry path. Every other repository
	// needs repos.<name>.path in ~/.tyci/config.json (#218).
	var watched []flow.RepoInfo
	var skipped []string
	for _, name := range dc.Repos {
		if repoErr == nil && name == info.Repo {
			watched = append(watched, info)
			continue
		}
		ri, err := flow.OpenRepo(home, name)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("repo %s skipped: %v", name, err))
			continue
		}
		watched = append(watched, ri)
	}
	if len(watched) > 0 {
		// Hooks, Lua tools, the cron dir and MCP servers are global to the
		// process, so their project-local part comes only from the repository
		// of --dir (a known limit of #218).
		local := flow.RepoInfo{}
		if repoErr == nil {
			local = info
		}
		agentCtx, shutdown := setupWorkflowAgents(ctx, cmd, local)
		defer shutdown()
		for _, ri := range watched {
			r, err := daemonRepo(agentCtx, ri, repoErr == nil && ri.Repo == info.Repo, logf)
			if err != nil {
				return err
			}
			cfg.Repos = append(cfg.Repos, r)
		}
	}
	d = daemon.New(cfg)
	for _, line := range skipped {
		d.Logf("%s", line)
	}
	if len(cfg.Repos) == 0 {
		d.Logf("no repository to watch: the issue and ci_failure triggers are off, cron only")
	}
	// A run has no model turn of its own, so no one reads the background
	// notices. The daemon writes them to its log.
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				for _, n := range drainNotices() {
					d.Logf("notice: %s", n)
				}
			}
		}
	}()
	return d.Run(ctx)
}

// daemonRepo wires one repository: the forge, the orchestrator config and a
// flow.Manager like 'tyci workflow run' has. agentCtx comes from
// setupWorkflowAgents. primary is true for the repository of --dir.
func daemonRepo(agentCtx context.Context, info flow.RepoInfo, primary bool, logf func(string, ...any)) (daemon.Repo, error) {
	fc, err := flowconfig.Load(info.Home, info.Root, info.Trusted)
	if err != nil {
		return daemon.Repo{}, err
	}
	oc, err := loadOrchestratorConfig(info)
	if err != nil {
		return daemon.Repo{}, err
	}
	// An explicit forge.repo applies only to the repository of --dir (for
	// example the upstream of a fork). A registry repository always polls its
	// own forge repository, so two repositories never poll the same one (#218).
	fcfg := fc.Forge
	if !primary || fcfg.Repo == "" || fcfg.Repo == "auto" {
		fcfg.Repo = info.Repo
	}
	f, err := newForge(fcfg, fc.Repos, info.Repo)
	if err != nil {
		return daemon.Repo{}, err
	}
	ciWorkflow := os.Getenv("TYCI_CI_WORKFLOW")
	if ciWorkflow == "" {
		ciWorkflow = "tests.yaml" // the default of ci_failure.sh
	}
	r := daemon.Repo{
		Name: info.Repo, Forge: f, AcceptedLabel: oc.AcceptedLabel, Workflow: orchestratorWorkflow(oc),
		DefaultBranch: info.DefaultBranch, CIWorkflow: ciWorkflow,
	}
	r.CI, _ = f.(forge.CIReader)
	r.Comments, _ = f.(forge.Commenter)

	m := flow.NewManager(func(text string) { logf("%s: %s", info.Repo, text) }, workflowSpawn)
	m.Info = func() (flow.RepoInfo, error) { return info, nil }
	// A signal stops the polling, not the runs: a cancelled run is saved as
	// failed. Runs that outlive the daemon are continued at the next start.
	m.SetBase(context.WithoutCancel(agentCtx))
	r.Runs = daemon.NewManagerRuns(m, info.Home, info.Repo)
	return r, nil
}

// cronLog writes the cron tick output to the daemon log, without the
// "no jobs due" line of every minute.
type cronLog func(string, ...any)

func (l cronLog) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" && line != "no jobs due" {
			l("cron: %s", line)
		}
	}
	return len(p), nil
}

func init() {
	daemonCmd.Flags().StringVar(&daemonDir, "dir", "", "project directory (default: current directory)")
	rootCmd.AddCommand(daemonCmd)
}

// daemonClient returns a client of the running daemon, or nil when there is
// no socket.
func daemonClient(home string) *daemon.Client {
	path := daemon.SocketPath(daemon.DefaultDir(home))
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return &daemon.Client{Path: path, Timeout: 2 * time.Second}
}

// daemonOrLocal is a daemon.Backend that asks the running daemon over its
// socket. Without a socket, or when the socket does not answer (a dead
// daemon), it asks local. An error that the daemon answers is returned.
type daemonOrLocal struct {
	home  string
	local daemon.Backend
}

var _ daemon.Backend = daemonOrLocal{}

func viaDaemon[T any](home string, remote func(*daemon.Client) (T, error), local func() (T, error)) (T, error) {
	if c := daemonClient(home); c != nil {
		v, err := remote(c)
		var re *daemon.RemoteError
		if err == nil || errors.As(err, &re) {
			return v, err
		}
	}
	return local()
}

// Status implements daemon.Backend.
func (b daemonOrLocal) Status(ctx context.Context) (daemon.Status, error) {
	return viaDaemon(b.home,
		func(c *daemon.Client) (daemon.Status, error) { return c.Status(ctx) },
		func() (daemon.Status, error) { return b.local.Status(ctx) })
}

// RunsList implements daemon.Backend.
func (b daemonOrLocal) RunsList(ctx context.Context) ([]daemon.RunInfo, error) {
	return viaDaemon(b.home,
		func(c *daemon.Client) ([]daemon.RunInfo, error) { return c.RunsList(ctx) },
		func() ([]daemon.RunInfo, error) { return b.local.RunsList(ctx) })
}

// RunsGet implements daemon.Backend. An answer without a state also falls
// back to local.
func (b daemonOrLocal) RunsGet(ctx context.Context, run string) (daemon.RunDetail, error) {
	return viaDaemon(b.home,
		func(c *daemon.Client) (daemon.RunDetail, error) {
			got, err := c.RunsGet(ctx, run)
			if err == nil && got.State == nil {
				err = errors.New("daemon answered no state")
			}
			return got, err
		},
		func() (daemon.RunDetail, error) { return b.local.RunsGet(ctx, run) })
}

// findRun reads a run through the daemon when its socket is there, else from
// the run files. A socket that does not answer (a dead daemon) falls back to
// the files.
func findRun(ctx context.Context, home, run string) (*flow.RunState, string, error) {
	got, err := daemonOrLocal{home: home, local: &daemon.InProcess{Home: home}}.RunsGet(ctx, run)
	var re *daemon.RemoteError
	if errors.As(err, &re) {
		return nil, "", fmt.Errorf("%s", re.Message)
	}
	if err != nil {
		return nil, "", err
	}
	return got.State, got.Path, nil
}
