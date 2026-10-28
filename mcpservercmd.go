package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/crazy-goat/tyci-agent/internal/daemon"
	"github.com/crazy-goat/tyci-agent/internal/mcpserver"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Model Context Protocol (MCP) server of tyci",
	Args:  cobra.NoArgs,
}

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve read-only tyci tools to an MCP client over stdin and stdout",
	Long: `Serve read-only tyci tools over MCP (JSON-RPC 2.0, one message per line on stdin
and stdout). An MCP client starts the command, for example:

  claude mcp add tyci -- tyci mcp serve

Tools:
  tyci_runs_list   the running and paused workflow runs, newest first
  tyci_run_status  the saved state of one run (argument run: the run id)

When 'tyci daemon' runs, the tools ask the daemon over its socket, and
tyci_runs_list lists the runs of the daemon repositories. Without a daemon, the
tools read the run files under ~/.tyci/runs: tyci_runs_list lists the runs of
the repository of the current directory, and tyci_run_status finds a run of
any repository. There are no tools that change a run. Logs go to stderr only.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		log := cmd.ErrOrStderr()
		local := &daemon.InProcess{Home: home}
		if info, err := workflowRepoInfo(""); err == nil {
			local.Repos = []string{info.Repo}
		} else {
			_, _ = fmt.Fprintf(log, "tyci mcp: %v: without a daemon, tyci_runs_list lists no runs\n", err)
		}
		s := &mcpserver.Server{Backend: daemonOrLocal{home: home, local: local}, Version: rootCmd.Version, Log: log}
		return s.Serve(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout())
	},
}

func init() {
	mcpCmd.AddCommand(mcpServeCmd)
	rootCmd.AddCommand(mcpCmd)
}
