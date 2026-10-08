package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// workflow — the CLI entry point for the project's workflows. Only "eject"
// is left: the JSON workflows (internal/flow) are run by the orchestrator.
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

func init() {
	workflowEjectCmd.Flags().StringVar(&workflowDir, "dir", "", "project directory (default: current directory)")
	workflowEjectCmd.Flags().BoolVar(&workflowEjectForce, "force", false, "overwrite existing files and role prompts")
	workflowCmd.AddCommand(workflowEjectCmd)
	rootCmd.AddCommand(workflowCmd)
}
