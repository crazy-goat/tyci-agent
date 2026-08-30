package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/internal/trust"
	"github.com/decodo/tyci/internal/workflow"
	"github.com/decodo/tyci/session"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// workflow — the CLI entry point for the Lua orchestration engine
// (internal/workflow). Wires TODO.md item 7: the engine was previously only
// reachable from its own test.
// ---------------------------------------------------------------------------

var workflowCmd = &cobra.Command{
	Use:   "workflow",
	Short: "Run Lua orchestration scripts",
}

var workflowDir string
var workflowPrompt string

var workflowRunCmd = &cobra.Command{
	Use:   "run <name-or-path>",
	Short: "Run a Lua workflow script",
	Long: `Run a Lua workflow script through the orchestration engine
(internal/workflow), giving it tyci.run_tool/new_session/resume_session and
the tyci.subagent/tyci.wait fan-out sugar.

<name-or-path> is either the name of a script discovered the same way named
agent definitions are (./.tyci/agents/<name>.lua project-local, falling back
to ~/.tyci/agents/<name>.lua, project-local winning on a name collision), or
a path to a .lua file, used directly when it exists.

The project-local .tyci/agents/*.lua directory is only consulted once this
project has been decided trusted (internal/trust, the same gate
.tyci/tools/*.lua and .tyci/cron.json go through) — an untrusted project
still runs a script named by an explicit path (you typed it), but name-based
discovery falls back to the global ~/.tyci/agents directory only. This is a
one-shot CLI invocation (like "tyci run"), so the trust decision never
blocks on an interactive prompt: an unknown project is treated as untrusted.

Like "tyci run"/"console"/"tui", this command loads project-local hooks
(.tyci/hooks.json), project-local Lua tools (.tyci/tools/*.lua), the local
cron dir, and connects MCP servers (.tyci/mcp.json) — all gated on the same
trust decision this command already makes for script discovery (see
workflowTrustedDirs), via the shared setupProjectLocalEnv helper
(commands.go) that initCommon also calls. --no-mcp opts out of the MCP
connect, same as for "tyci run".`,
	Args: cobra.ExactArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		_, dirs := workflowTrustedDirs()
		return workflow.ListScriptsIn(dirs), cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		// workflowTrustedDirs makes the one trust decision this whole
		// invocation uses (non-interactive, same posture as `tyci run`):
		// both for whether NAME-based script discovery may resolve into the
		// project-local .tyci/agents directory below, and for the
		// project-local hooks/Lua-tools/cron-dir/MCP setup further down
		// (setupProjectLocalEnv). Decided once, unconditionally — including
		// for an explicit .lua path, which bypasses only the script-
		// discovery trust gate, not this one.
		trusted, dirs := workflowTrustedDirs()

		// An explicit .lua path is a deliberate, direct instruction — the
		// caller typed the exact file to run, no name-based discovery
		// involved — so it bypasses the trust gate entirely, the same way
		// ResolveScript/ResolveScriptIn special-case it. Trust only governs
		// whether NAME-based discovery may resolve into the project-local
		// .tyci/agents directory.
		path, explicit := workflowExplicitPath(args[0])
		if !explicit {
			if !trusted {
				fmt.Fprintln(os.Stderr,
					"tyci: this project is not trusted — project-local workflow scripts "+
						"(.tyci/agents/*.lua) are skipped this session. Global ~/.tyci/agents "+
						"content still loads as usual. Run tyci in an interactive mode "+
						"(console/tui) in this directory to be asked, or edit ~/.tyci/trust.json directly.")
			}
			resolved, err := workflow.ResolveScriptIn(dirs, args[0])
			if err != nil {
				return err
			}
			path = resolved
		}

		// Every other agent-running path (initCommon, used by `run`/
		// `console`/`tui`) registers providers before anything tries to
		// resolve a model. Without this, session:await() fails with "model
		// not found" unless the script happens to call tyci.models() first
		// (which has the side effect of registering providers) — an easy
		// trap for a script that never calls tyci.models() at all.
		registerProviders()

		// Project-local hooks/Lua tools/cron dir/MCP (F28): the same
		// environment initCommon wires up for `run`/`console`/`tui`, gated
		// on the same `trusted` value decided above rather than deciding
		// trust again here (see setupProjectLocalEnv's doc comment on why
		// that would risk two different answers and a duplicate warning).
		wd := workflowDir
		if wd == "" {
			wd, _ = os.Getwd()
		}
		noMCP, _ := cmd.Flags().GetBool("no-mcp")
		ctx, shutdown := setupProjectLocalEnv(cmd.Context(), wd, trusted, true, noMCP)
		defer shutdown()

		engine := workflow.NewEngine(ctx, workflowPrompt)
		engine.WorkDir = workflowDir
		result, err := engine.Run(path)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), result)
		printWorkflowLedgerSummary(cmd.ErrOrStderr())
		return nil
	},
}

var workflowListCmd = &cobra.Command{
	Use:   "list",
	Short: "List discoverable workflow scripts",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, dirs := workflowTrustedDirs()
		names := workflow.ListScriptsIn(dirs)
		if len(names) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "No workflow scripts found.")
			return nil
		}
		for _, name := range names {
			fmt.Fprintln(cmd.OutOrStdout(), name)
		}
		return nil
	},
}

// printWorkflowLedgerSummary prints a one-line token/cost summary of what
// this invocation's named-agent sessions spent, to w (stderr — see F32's
// caller: stdout carries the script's own printed result, which a caller
// may pipe or parse, so this must never touch it).
//
// `tyci workflow run` is a one-shot CLI process, not the TUI — the ledger
// (internal/ledger) it records into (see engine.go's ledger.Watch wrap of
// the session's sink) has, today, exactly two readers, both in package
// display (tui_tokens.go, tui_sidebar.go), and neither runs here. Without
// this, a workflow session's spend would be recorded but never surfaced —
// an invisible fix. display's own formatting (formatCost et al.) is
// unexported and built for the TUI's width-constrained status bar
// (truncation, lipgloss wrapping); reusing it would mean dragging that
// machinery, and the TUI's own weight, into this CLI path for no benefit,
// so this formats locally instead, matching the token/cost convention
// display/tui_tokens.go's Tokens-tab breakdown uses (a row's tokens are
// Usage.Input+Usage.Output; the dollar figure is Snapshot.TotalUSD()).
//
// Prints nothing when the snapshot is empty — a script that ran no agent
// session (never called tyci.new_session/resume_session, or never awaited
// one) has nothing to report, and a "$0.00" line for that would be noise.
func printWorkflowLedgerSummary(w io.Writer) {
	snap := ledger.Get()
	if len(snap.Rows) == 0 {
		return
	}
	var tokens int
	for _, r := range snap.Rows {
		tokens += r.Usage.Input + r.Usage.Output
	}
	fmt.Fprintf(w, "tyci workflow: %d tokens, $%.2f\n", tokens, snap.TotalUSD())
}

// workflowExplicitPath reports whether name is an existing .lua file path
// rather than a bare name for discovery — the same check
// workflow.ResolveScriptIn makes internally, duplicated here (cheaply: one
// os.Stat) so the CLI can decide whether to consult trust at all before
// calling into ResolveScriptIn.
func workflowExplicitPath(name string) (path string, ok bool) {
	if !strings.HasSuffix(name, ".lua") {
		return "", false
	}
	info, err := os.Stat(name)
	if err != nil || info.IsDir() {
		return "", false
	}
	return name, true
}

// workflowTrustedDirs decides whether the current project is trusted
// (internal/trust, non-interactive: this is a one-shot CLI invocation, so an
// unknown project is treated as untrusted rather than blocking on a prompt —
// the same posture initCommon takes for `tyci run`) and returns the agents
// directories a workflow script may be discovered in accordingly: both
// ~/.tyci/agents and <project>/.tyci/agents when trusted, only the global
// one otherwise.
//
// Without this gate, ResolveScript/ListScripts would let a project-local
// .tyci/agents/*.lua script — arbitrary code checked into (or dropped into)
// a repo nobody has vetted — run, and even silently shadow a same-named
// global script, purely by virtue of `tyci workflow run <name>` being
// invoked from that directory. That is exactly the shape of risk
// .tyci/tools/*.lua and .tyci/cron.json are already gated on (see
// commands.go's initCommon and croncmd.go's cronDirs).
func workflowTrustedDirs() (trusted bool, dirs []string) {
	wd := workflowDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	projectRoot, _ := session.ProjectKey(wd)
	trusted, _, err := trust.Decide(projectRoot, false, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: trust: %v\n", err)
	}
	if trusted {
		return true, agentdefs.Dirs(wd)
	}
	return false, []string{agentdefs.GlobalDir()}
}

func init() {
	workflowRunCmd.Flags().StringVar(&workflowPrompt, "prompt", "", "Prompt made available to the script as the global `prompt`")
	workflowRunCmd.Flags().StringVar(&workflowDir, "dir", "", "project directory to resolve project-local .tyci/agents from (default: current directory)")
	workflowListCmd.Flags().StringVar(&workflowDir, "dir", "", "project directory to resolve project-local .tyci/agents from (default: current directory)")
	workflowCmd.AddCommand(workflowRunCmd, workflowListCmd)
	rootCmd.AddCommand(workflowCmd)
}
