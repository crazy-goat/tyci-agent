package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/tools"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// workflow — the CLI entry point for the project's workflows. The JSON
// workflows (internal/flow) run headless with run, status and validate, and
// eject copies a builtin workflow into the project.
// ---------------------------------------------------------------------------

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Manage the project's workflows",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return cmd.Help()
	},
}

var workflowDir string

var workflowEjectForce bool

var workflowEjectCmd = &cobra.Command{
	Use:   "eject <name>",
	Short: "Copy a builtin workflow into the project's .tyci/ to change it",
	Long: `Copy the builtin workflow <name> (for example issue-to-merge) into
.tyci/ of the project: workflows/<name>.json, the check scripts in checks/,
the task templates in tasks/ and the role prompts in prompts/. It sets
roles.<role>.prompt to "@prompts/<role>.md" in .tyci/config.json.

The project is the git repository of --dir (default: the current directory).
tyci uses these files only in a trusted project. Existing files are not
overwritten unless --force is given.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := workflowDir
		if dir == "" {
			dir, _ = os.Getwd()
		}
		if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
			dir = strings.TrimSpace(string(out))
		}
		written, err := flow.Eject(args[0], dir, workflowEjectForce)
		for _, p := range written {
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", p)
		}
		return err
	},
}

var (
	workflowRunJSON      bool
	workflowRunDir       string
	workflowValidateJSON bool
	workflowValidateDir  string
	workflowStatusJSON   bool
)

// workflowRepoInfo finds the repository of a workflow command. Tests replace it,
// because a real run needs a GitHub origin.
var workflowRepoInfo = flow.DetectRepoAt

// workflowSpawn runs one agent of a workflow run. Tests replace it with a stub.
var workflowSpawn = tools.RunSubagentTask

// workflowResult is the one JSON object that the headless workflow commands
// print on stdout with --json.
type workflowResult struct {
	RunID     string         `json:"run_id,omitempty"`
	Workflow  string         `json:"workflow,omitempty"`
	State     string         `json:"state,omitempty"`
	Status    string         `json:"status,omitempty"` // running|paused|done|failed
	Visits    map[string]int `json:"visits,omitempty"`
	StateFile string         `json:"state_file,omitempty"`
	OK        *bool          `json:"ok,omitempty"` // validate only
	Errors    []string       `json:"errors,omitempty"`
	Error     string         `json:"error,omitempty"`
}

var workflowRunCmd = &cobra.Command{
	Use:   "run <name> <issue>",
	Short: "Run a workflow for an issue until it ends or pauses at an ask state",
	Long: `Run the workflow <name> for the GitHub issue <issue> of the repository of --dir
(default: the current directory). The command waits until the run is done, failed or
paused at an ask state. It never prompts for an answer. A paused run stays paused.

With --json the command prints one JSON object on stdout. Progress and warnings go to
stderr. Exit code 0 means done or paused. Exit code 1 means failed or invalid.`,
	Args: jsonArgs(2, &workflowRunJSON),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut := workflowRunJSON
		issue, err := strconv.Atoi(args[1])
		if err != nil || issue <= 0 {
			return fail(cmd, jsonOut, fmt.Errorf("issue must be a positive number: %s", args[1]))
		}
		info, err := workflowRepoInfo(workflowRunDir)
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		noteUntrusted(cmd, info)
		warnings, problems, err := flow.CheckWorkflow(info, args[0])
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		printWarnings(cmd, warnings)
		if len(problems) > 0 {
			return printValidity(cmd.OutOrStdout(), jsonOut, args[0], problems)
		}

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		ctx, shutdown := setupWorkflowAgents(ctx, cmd, info)
		defer shutdown()
		m := flow.NewManager(func(text string) { fmt.Fprintln(cmd.ErrOrStderr(), text) }, workflowSpawn)
		m.Info = func() (flow.RepoInfo, error) { return info, nil }
		m.SetBase(ctx)
		events := make(chan flow.RunEvent, 16)
		unsubscribe := m.Subscribe(func(ev flow.RunEvent) { events <- ev })
		defer unsubscribe()

		runID, _, err := m.Start(ctx, flow.StartRequest{Workflow: args[0], Issue: issue})
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		// The command context, not ctx: Ctrl+C must let the runner save its
		// final state first. Only a deadline (set by tests) ends the wait early.
		if err = awaitRun(cmd.Context(), events, runID); err != nil {
			return fail(cmd, jsonOut, fmt.Errorf("run %s did not report its end: %w", runID, err))
		}
		runDir := flow.RunDir(info.Home, info.Name(), runID)
		st, err := flow.Load(runDir)
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		r := runResult(st, flow.StatePath(runDir))
		printResult(cmd.OutOrStdout(), jsonOut, r)
		if r.Error != "" {
			return errors.New(r.Error)
		}
		if st.Status == "paused" {
			fmt.Fprintf(cmd.ErrOrStderr(), "waiting at ask state %s\n", st.Current)
		}
		return nil
	},
}

var workflowValidateCmd = &cobra.Command{
	Use:   "validate <name>",
	Short: "Check a workflow the way a run checks it, without starting a run",
	Long: `Check the workflow <name> for the repository of --dir (default: the current
directory). Project workflows and roles are used only in a trusted project.
Exit code 0 means valid. Exit code 1 means invalid or unknown.`,
	Args: jsonArgs(1, &workflowValidateJSON),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut := workflowValidateJSON
		info, err := workflowRepoInfo(workflowValidateDir)
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		noteUntrusted(cmd, info)
		warnings, problems, err := flow.CheckWorkflow(info, args[0])
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		printWarnings(cmd, warnings)
		return printValidity(cmd.OutOrStdout(), jsonOut, args[0], problems)
	},
}

var workflowStatusCmd = &cobra.Command{
	Use:   "status <run-id>",
	Short: "Show the saved state of a run in any repository",
	Long: `Show the saved state of the run <run-id>. The command searches the runs of every
repository under ~/.tyci/runs. Exit code 0 means running, paused or done. Exit code 1
means failed, unknown or ambiguous.`,
	Args: jsonArgs(1, &workflowStatusJSON),
	RunE: func(cmd *cobra.Command, args []string) error {
		jsonOut := workflowStatusJSON
		home, err := os.UserHomeDir()
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		st, statePath, err := flow.FindRun(home, args[0])
		if err != nil {
			return fail(cmd, jsonOut, err)
		}
		r := runResult(st, statePath)
		printResult(cmd.OutOrStdout(), jsonOut, r)
		if r.Error != "" {
			return errors.New(r.Error)
		}
		return nil
	},
}

// awaitRun returns when run id reports its final status (done, failed or paused).
// It returns the error of ctx when ctx ends first, so a run that never reports
// does not block the command.
func awaitRun(ctx context.Context, events <-chan flow.RunEvent, id string) error {
	for {
		select {
		case ev := <-events:
			if ev.Run == id && ev.Status != "running" {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// setupWorkflowAgents gives the agents of a run the environment that tyci run
// gives its agent: the providers, the retry and timeout settings, and the
// project-local hooks, Lua tools, cron dir and MCP servers. info.Trusted decides
// the project-local parts. MCP servers are connected, as in tyci run, so an agent
// has the same tools in every mode. The returned shutdown must be deferred.
func setupWorkflowAgents(ctx context.Context, cmd *cobra.Command, info flow.RepoInfo) (context.Context, func()) {
	registerProviders()
	maxRetries, _ := cmd.Flags().GetInt("max-retries")
	setRetryConfig(maxRetries)
	noMCP, _ := cmd.Flags().GetBool("no-mcp")
	return setupProjectLocalEnv(ctx, info.Root, info.Trusted, true, noMCP)
}

// runResult maps a saved run state to the result object. A failed run carries
// its reason in error.
func runResult(st *flow.RunState, statePath string) workflowResult {
	r := workflowResult{
		RunID:     st.Run,
		Workflow:  st.Workflow,
		State:     st.Current,
		Status:    st.Status,
		Visits:    st.Visits,
		StateFile: statePath,
	}
	if st.Status == "failed" {
		r.Error = st.Reason
		if r.Error == "" {
			r.Error = "run failed"
		}
	}
	return r
}

// printValidity prints the check of workflow name and returns an error when the
// workflow is invalid.
func printValidity(w io.Writer, jsonOut bool, name string, problems []string) error {
	ok := len(problems) == 0
	printResult(w, jsonOut, workflowResult{Workflow: name, OK: &ok, Errors: problems})
	if !ok {
		return fmt.Errorf("workflow %s is invalid", name)
	}
	return nil
}

// printResult writes r as one JSON object, or as short text when jsonOut is false.
// A result with only an error has no text form: the caller returns the error, and
// main prints it on stderr.
func printResult(w io.Writer, jsonOut bool, r workflowResult) {
	if jsonOut {
		_ = json.NewEncoder(w).Encode(r)
		return
	}
	switch {
	case r.OK != nil && *r.OK:
		fmt.Fprintf(w, "workflow %s is valid\n", r.Workflow)
	case r.OK != nil:
		fmt.Fprintf(w, "workflow %s is invalid\n", r.Workflow)
		for _, e := range r.Errors {
			fmt.Fprintf(w, "  %s\n", e)
		}
	case r.RunID != "":
		fmt.Fprintf(w, "run %s: %s at state %s\n", r.RunID, r.Status, r.State)
	}
}

// fail prints err as the result of the command (with --json as {"error": ...}) and
// returns it, so the exit code is 1.
func fail(cmd *cobra.Command, jsonOut bool, err error) error {
	printResult(cmd.OutOrStdout(), jsonOut, workflowResult{Error: err.Error()})
	return err
}

// jsonArgs checks the number of positional arguments. A failure is reported like
// any other error of the command.
func jsonArgs(n int, jsonOut *bool) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return fail(cmd, *jsonOut, err)
		}
		return nil
	}
}

// noteUntrusted says that an untrusted project does not contribute its own workflows.
func noteUntrusted(cmd *cobra.Command, info flow.RepoInfo) {
	if !info.Trusted {
		fmt.Fprintln(cmd.ErrOrStderr(), "tyci: this project is not trusted, so its .tyci/workflows are skipped. "+
			"Run tyci tui in this directory to be asked, or edit ~/.tyci/trust.json.")
	}
}

func printWarnings(cmd *cobra.Command, warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
	}
}

func init() {
	workflowEjectCmd.Flags().StringVar(&workflowDir, "dir", "", "project directory (default: current directory)")
	workflowEjectCmd.Flags().BoolVar(&workflowEjectForce, "force", false, "overwrite existing files and role prompts")
	workflowRunCmd.Flags().BoolVar(&workflowRunJSON, "json", false, "print one JSON object on stdout")
	workflowRunCmd.Flags().StringVar(&workflowRunDir, "dir", "", "project directory (default: current directory)")
	workflowValidateCmd.Flags().BoolVar(&workflowValidateJSON, "json", false, "print one JSON object on stdout")
	workflowValidateCmd.Flags().StringVar(&workflowValidateDir, "dir", "", "project directory (default: current directory)")
	workflowStatusCmd.Flags().BoolVar(&workflowStatusJSON, "json", false, "print one JSON object on stdout")
	workflowCmd.AddCommand(workflowEjectCmd, workflowRunCmd, workflowValidateCmd, workflowStatusCmd)
	rootCmd.AddCommand(workflowCmd)
}
