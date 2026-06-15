package main

import (
	"github.com/spf13/cobra"
)

// rootCmd is the top-level `tyci` command.
var rootCmd = &cobra.Command{
	Use:   "tyci",
	Short: "tyci — CLI tool that runs AI agents powered by LLMs",
	Long: `tyci runs AI agents powered by large language models with a multi-turn
agent loop, tool execution, session persistence, and streaming responses.

Run 'tyci <subcommand> --help' for details on a specific subcommand.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// No RunE — invoking `tyci` with no subcommand prints help and exits 0,
	// which matches the behaviour of the previous flag-based dispatcher.
}

// runCmd, consoleCmd, tuiCmd share the same flag set. The actual logic lives
// in runSubcommandWithOpts in cmd_interactive.go.
func newRunStyleCmd(use, short, kind string) *cobra.Command {
	opts := &runCmdOptions{Kind: kind}

	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kind == "run" && opts.Prompt == "" {
				return errRunRequiresPrompt
			}
			return runSubcommandWithOpts(*opts)
		},
	}

	pflags := cmd.Flags()
	pflags.StringVar(&opts.Model, "model", "", "Model to use (format: provider/model)")
	pflags.StringVar(&opts.Agent, "agent", "", "Agent name to use for default model (from agents config)")
	pflags.StringVar(&opts.Prompt, "prompt", "", "Prompt for response (required for `run`)")
	pflags.IntVar(&opts.MaxRetries, "max-retries", 5, "Max retries on transient errors (0 to disable)")
	pflags.IntVar(&opts.MaxIterations, "max-iterations", -1, "Max tool-call iterations (-1 = unlimited)")
	pflags.StringVar(&opts.HistoryFile, "history-file", "", "Path to history file (default: ~/.tyci/history)")
	pflags.StringVar(&opts.Session, "session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	pflags.BoolVar(&opts.NoSession, "no-session", false, "Disable session persistence")
	pflags.BoolVar(&opts.Debug, "debug", false, "Show HTTP request/response data")
	pflags.BoolVar(&opts.NoDebug, "no-debug", false, "Disable API request/response debug logging")

	cmd.RegisterFlagCompletionFunc("model", completeProviderModels)
	cmd.RegisterFlagCompletionFunc("agent", completeAgents)

	return cmd
}

// agentCmd is the `tyci agent` dispatcher and parent of its subcommands.
var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent configurations",
	Long:  "Manage named agent configurations (list|get|set|delete|set-fallback).",
	RunE: func(_ *cobra.Command, _ []string) error {
		// `tyci agent` with no subcommand lists agents (preserves old behaviour).
		return runAgentList()
	},
}

func newAgentGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show agent model assignment",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentGet(args[0])
		},
		ValidArgsFunction: completeAgents,
	}
}

func newAgentSetCmd() *cobra.Command {
	var model string
	cmd := &cobra.Command{
		Use:   "set <name>",
		Short: "Assign a model to an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if model == "" {
				return errAgentSetRequiresModel
			}
			return runAgentSet(args[0], model)
		},
		ValidArgsFunction: completeAgents,
	}
	cmd.Flags().StringVarP(&model, "model", "m", "", "Model string (format: provider/model)")
	cmd.RegisterFlagCompletionFunc("model", completeProviderModels)
	return cmd
}

func newAgentDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Remove an agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentDelete(args[0])
		},
		ValidArgsFunction: completeAgents,
	}
}

func newAgentSetFallbackCmd() *cobra.Command {
	var models []string
	cmd := &cobra.Command{
		Use:   "set-fallback <name>",
		Short: "Set fallback models for an agent (repeat --model to set several)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runAgentSetFallback(args[0], models)
		},
		ValidArgsFunction: completeAgents,
	}
	cmd.Flags().StringArrayVarP(&models, "model", "m", nil, "Fallback model (repeat for multiple)")
	cmd.RegisterFlagCompletionFunc("model", completeProviderModels)
	return cmd
}

// providerCmd is the `tyci provider` dispatcher and parent of its subcommands.
var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Manage providers and auth",
	Long:  "Manage providers and auth (add|refresh|auth).",
	RunE: func(_ *cobra.Command, _ []string) error {
		return errProviderUsage
	},
}

func newProviderAddCmd() *cobra.Command {
	var (
		apiType string
		url     string
		token   string
		test    bool
		testMod string
	)
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a provider with auth and connectivity check",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runProviderAdd(args[0], apiType, url, token, test, testMod)
		},
	}
	cmd.Flags().StringVar(&apiType, "api", "openai", "API type (openai, anthropic, gemini)")
	cmd.Flags().StringVar(&url, "url", "", "API base URL")
	cmd.Flags().StringVar(&token, "token", "", "API key or $ENV_VAR reference")
	cmd.Flags().BoolVar(&test, "test", false, "Test connectivity after adding")
	cmd.Flags().StringVar(&testMod, "test-model", "", "Model to test with (default: first model)")
	return cmd
}

func newProviderRefreshCmd() *cobra.Command {
	var (
		providerFilter string
		dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Import models from models.dev",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runProviderRefresh(providerFilter, dryRun)
		},
	}
	cmd.Flags().StringVar(&providerFilter, "provider", "", "Comma-separated list of providers to import (default: all)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview without writing")
	cmd.RegisterFlagCompletionFunc("provider", completeProviderNames)
	return cmd
}

// providerAuthCmd is `tyci provider auth` parent.
var providerAuthCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage API keys in ~/.tyci/auth.json",
	RunE: func(_ *cobra.Command, _ []string) error {
		return errProviderAuthUsage
	},
}

func newProviderAuthSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <provider> [<key>]",
		Short: "Store an API key for a provider (use '-' to read key from stdin)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				return runProviderAuthSet(args[0], "")
			}
			return runProviderAuthSet(args[0], args[1])
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			// Only suggest provider names for the first positional.
			if len(args) == 0 {
				return completeProviderNames(nil, nil, toComplete)
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
	}
}

func newProviderAuthGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "get <provider>",
		Short:             "Print the stored API key (masked) for a provider",
		Args:              cobra.ExactArgs(1),
		RunE:              func(_ *cobra.Command, args []string) error { return runProviderAuthGet(args[0]) },
		ValidArgsFunction: completeProviderNames,
	}
}

func newProviderAuthListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List providers that have stored API keys",
		RunE:  func(_ *cobra.Command, _ []string) error { return runProviderAuthList() },
	}
}

func newProviderAuthRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "rm <provider>",
		Short:             "Remove the stored API key for a provider",
		Args:              cobra.ExactArgs(1),
		RunE:              func(_ *cobra.Command, args []string) error { return runProviderAuthRm(args[0]) },
		ValidArgsFunction: completeProviderNames,
	}
}

func init() {
	// Top-level run modes.
	rootCmd.AddCommand(
		newRunStyleCmd("run", "One-shot run with a single --prompt (minimal display)", "run"),
		newRunStyleCmd("console", "Interactive REPL with readline, history, slash commands", "console"),
		newRunStyleCmd("tui", "Bubble Tea TUI with model picker, split-pane, mouse support", "tui"),
	)

	// Agent subcommands.
	agentCmd.AddCommand(
		&cobra.Command{Use: "list", Short: "List all agents", RunE: func(_ *cobra.Command, _ []string) error { return runAgentList() }},
		newAgentGetCmd(),
		newAgentSetCmd(),
		newAgentDeleteCmd(),
		newAgentSetFallbackCmd(),
	)
	rootCmd.AddCommand(agentCmd)

	// Provider subcommands.
	providerAuthCmd.AddCommand(
		newProviderAuthSetCmd(),
		newProviderAuthGetCmd(),
		newProviderAuthListCmd(),
		newProviderAuthRmCmd(),
	)
	providerCmd.AddCommand(
		newProviderAddCmd(),
		newProviderRefreshCmd(),
		providerAuthCmd,
	)
	rootCmd.AddCommand(providerCmd)
}
