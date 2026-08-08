package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/internal/skills"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// ---------------------------------------------------------------------------
// Root command
// ---------------------------------------------------------------------------

var rootCmd = &cobra.Command{
	Use:           "tyci",
	Short:         "LLM-powered AI agent CLI",
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		// If args were given (unknown subcommand), show help.
		if len(args) > 0 {
			cmd.Help()
			return nil
		}
		// TUI requires a terminal; fall back to help when piped.
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			cmd.Help()
			return nil
		}
		// Default: launch the TUI.
		return tuiCmd.RunE(cmd, args)
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Persistent flags shared by run / console / tui
	rootCmd.PersistentFlags().String("model", "", "Model to use (format: provider/model)")
	rootCmd.PersistentFlags().String("agent", "", "Agent name to use for default model (from agents config)")
	rootCmd.PersistentFlags().Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	rootCmd.PersistentFlags().Int("max-iterations", -1, "Max tool-call iterations (-1 = unlimited)")
	rootCmd.PersistentFlags().String("history-file", "", "Path to history file (default: ~/.tyci/history)")
	rootCmd.PersistentFlags().String("session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	rootCmd.PersistentFlags().Bool("no-session", false, "Disable session persistence")
	rootCmd.PersistentFlags().Bool("debug", false, "Show HTTP request/response data")
	rootCmd.PersistentFlags().Bool("no-debug", false, "Disable API request/response debug logging")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(consoleCmd)
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(providerCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(completionCmd)
}

// ---------------------------------------------------------------------------
// Shared setup
// ---------------------------------------------------------------------------

func registerProviders() {
	if err := connect.EnsureProvidersJSON(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: providers.json: %v\n", err)
	}
	providers.RegisterProvidersFromProvidersJSON(connect.ProvidersJSONPath())
	providers.RegisterProvidersFromConfig(connect.ModelJSONPath())
}

func initCommon(cmd *cobra.Command) (providers.Provider, string, agent.Config, context.Context, *session.Session, string, string, *debug.Logger, error) {
	registerProviders()

	maxRetries, _ := cmd.Flags().GetInt("max-retries")
	providers.DefaultRetryConfig = api.RetryConfig{MaxRetries: maxRetries, BaseBackoff: 4, MaxBackoff: 128}

	model, _ := cmd.Flags().GetString("model")
	agentName, _ := cmd.Flags().GetString("agent")
	if model == "" {
		model = agent.ResolveModel("", agentName)
	}
	if model == "" {
		return nil, "", agent.Config{}, nil, nil, "", "", nil, fmt.Errorf("no model specified. Use --model, --agent, or configure a default agent")
	}

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		return nil, "", agent.Config{}, nil, nil, "", "", nil, fmt.Errorf("model %q not found", model)
	}

	if agentName == "" {
		agentName = "default"
	}
	var fallbacks []connector.ModelClient
	if fb := agent.GetFallbackModels(agentName); len(fb) > 0 {
		fallbacks = resolveFallbacks(fb)
	}

	var ctx context.Context
	var dl *debug.Logger
	noDebug, _ := cmd.Flags().GetBool("no-debug")
	if !noDebug {
		var err error
		dl, err = debug.Init()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: debug log: %v\n", err)
			ctx = context.Background()
		} else {
			ctx = debug.NewContext(context.Background(), dl)
		}
	} else {
		ctx = context.Background()
	}

	debugFlag, _ := cmd.Flags().GetBool("debug")
	maxIterations, _ := cmd.Flags().GetInt("max-iterations")
	cfg := agent.Config{
		System:        providers.BuildSystemPrompt(),
		MaxRetries:    maxRetries,
		MaxIterations: maxIterations,
		Debug:         debugFlag,
		Tools:         toolsAdapter{},
		Schema:        tools.GetToolsSchemaJSON(),
		Fallbacks:     fallbacks,
		PendingTodos:  tools.PendingTodos,
		HasTodos:      tools.HasPendingTodos,
	}
	ctx = connector.WithModelClient(ctx, provider.Client(modelName))

	wd, _ := os.Getwd()
	var sess *session.Session
	var sessionPath string
	noSession, _ := cmd.Flags().GetBool("no-session")
	if !noSession {
		explicitSession, _ := cmd.Flags().GetString("session")
		if explicitSession != "" {
			sessionPath = explicitSession
			// Explicit --session: open immediately so /resume-style workflow
			// works at startup (history replay, header inspection, etc.).
			var err error
			sess, err = session.Open(sessionPath, wd, modelName, provider.Name())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: session: %v (continuing without session)\n", err)
				sess = nil
				sessionPath = ""
			}
		} else {
			// Auto-generated session path. Resolve it but DON'T create the
			// file yet — that would leave an empty session.jsonl on disk for
			// every repl/TUI the user opens without ever typing a prompt.
			// The session is opened lazily by ensureSession() the moment we
			// are about to write user input (interactive.submitUserLine,
			// tui_mode before agent.Run, and runPrompt).
			var err error
			sessionPath, err = session.DefaultPath(wd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: cannot determine session path: %v\n", err)
				sessionPath = ""
			}
		}
	}
	cfg.Session = sess

	historyFile, _ := cmd.Flags().GetString("history-file")
	if historyFile == "" {
		hf, err := readline.DefaultHistoryFile()
		if err == nil {
			historyFile = hf
		}
	}

	return provider, modelName, cfg, ctx, sess, sessionPath, historyFile, dl, nil
}

// resolveFallbacks resolves each "provider/model" fallback spec to a
// connector.ModelClient at setup time — the agent no longer resolves
// fallback specs itself (see agent.Config.Fallbacks). A spec that fails to
// resolve is reported here and skipped, which is a deliberate relocation:
// agent.Run used to discover this lazily, mid-run, and report it via a
// ToolBlock on the display; now it is reported once, at startup, on stderr.
func resolveFallbacks(specs []string) []connector.ModelClient {
	var out []connector.ModelClient
	for _, spec := range specs {
		p, m, ok := providers.FindModel(spec)
		if !ok {
			fmt.Fprintf(os.Stderr, "Warning: fallback model %q not found, skipping\n", spec)
			continue
		}
		out = append(out, p.Client(m))
	}
	return out
}

// ---------------------------------------------------------------------------
// run
// ---------------------------------------------------------------------------

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a one-shot prompt",
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		if prompt == "" {
			return fmt.Errorf("--prompt is required")
		}
		provider, modelName, cfg, ctx, _, sessionPath, _, dl, err := initCommon(cmd)
		if err != nil {
			return err
		}
		if dl != nil {
			defer dl.Close()
		}
		// `tyci run` is a one-shot CLI invocation. Use the
		// bracket-prefix Minimal display so output is plain, one line
		// per event, and easy to grep / pipe. For the rich REPL or
		// full-screen experience, use `tyci console` or
		// `tyci tui` instead.
		disp := display.NewMinimal()
		// No resolver: a one-shot run has nowhere to type /model, so
		// SwitchModel is not reachable and does not need a catalog.
		cond := newConductor(provider, modelName, disp, cfg, sessionPath, nil)
		runPrompt(cond, disp, prompt, ctx)
		return nil
	},
}

func init() {
	runCmd.Flags().String("prompt", "", "Prompt for response (required)")
	runCmd.RegisterFlagCompletionFunc("model", completeProviderModels)
	runCmd.RegisterFlagCompletionFunc("agent", completeAgents)
}

// ---------------------------------------------------------------------------
// console
// ---------------------------------------------------------------------------

var consoleCmd = &cobra.Command{
	Use:   "console",
	Short: "Start an interactive console session",
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, modelName, cfg, ctx, _, sessionPath, historyFile, dl, err := initCommon(cmd)
		if err != nil {
			return err
		}
		if dl != nil {
			defer dl.Close()
		}
		disp := display.NewTerminal()
		// requireConfigured: /model in the console refuses a provider
		// without credentials and says how to add one.
		cond := newConductor(provider, modelName, disp, cfg, sessionPath, catalogResolver{requireConfigured: true})
		runInteractive(cond, disp, historyFile, ctx)
		return nil
	},
}

// ---------------------------------------------------------------------------
// tui
// ---------------------------------------------------------------------------

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Start the TUI (rich terminal UI)",
	RunE: func(cmd *cobra.Command, args []string) error {
		provider, modelName, cfg, ctx, _, sessionPath, historyFile, dl, err := initCommon(cmd)
		if err != nil {
			return err
		}
		if dl != nil {
			defer dl.Close()
		}

		authKeys, err := connect.ListKeys()
		authSet := make(map[string]bool)
		if err == nil {
			for _, k := range authKeys {
				authSet[k] = true
			}
		}

		var allModels []string
		var allProviderModels []display.ProviderModels
		for _, p := range providers.ListProviders() {
			if !authSet[p.Name()] {
				continue
			}
			pm := display.ProviderModels{Name: p.Name()}
			for _, m := range p.Models() {
				allModels = append(allModels, p.Name()+"/"+m)
				pm.Models = append(pm.Models, m)
			}
			if len(pm.Models) > 0 {
				allProviderModels = append(allProviderModels, pm)
			}
		}
		model, _ := cmd.Flags().GetString("model")
		if model == "" {
			agentName, _ := cmd.Flags().GetString("agent")
			model = agent.ResolveModel("", agentName)
		}

		// Load favorite models from config. The current model is added to the
		// Tab-cycle by the TUI at runtime (see switchModel); it is intentionally
		// NOT persisted here so toggling favorites can't silently save it.
		favorites := agent.GetFavoriteModels()

		// Compute context counts for the top status bar.
		toolsCount := len(tools.GetAllToolsSchema())
		skillNames, _ := skills.ListSkills(skills.SkillsDir())
		skillsCount := len(skillNames)
		mcpCount := 0
		if mcpRunner := tools.GetMCPToolRunner(); mcpRunner != nil {
			mcpCount = len(mcpRunner.MCPToolsSchema())
		}

		tuiDisp := display.NewTUI(model, historyFile, allModels, allProviderModels, favorites, func(mdl string, favorite bool) {
			if favorite {
				_ = agent.AddFavoriteModel(mdl)
			} else {
				_ = agent.RemoveFavoriteModel(mdl)
			}
		}, agent.GetDefaultModel(), func(newDefault string) {
			_ = agent.SetDefaultModel(newDefault)
		}, toolsCount, skillsCount, mcpCount)
		runTUI(provider, modelName, tuiDisp, cfg, ctx, sessionPath)
		return nil
	},
}

// ---------------------------------------------------------------------------
// provider add / refresh
// ---------------------------------------------------------------------------

var providerAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a custom provider (fetches models from the API)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		name := args[0]
		apiType, _ := cmd.Flags().GetString("api")
		baseURL, _ := cmd.Flags().GetString("url")
		token, _ := cmd.Flags().GetString("token")
		test, _ := cmd.Flags().GetBool("test")
		testModel, _ := cmd.Flags().GetString("test-model")
		return connect.AddProvider(name, apiType, baseURL, token, test, testModel)
	},
}

func init() {
	providerAddCmd.Flags().String("api", "openai", "API type (openai, anthropic, gemini)")
	providerAddCmd.Flags().String("url", "", "API base URL")
	providerAddCmd.Flags().String("token", "", "API key or $ENV_VAR reference")
	providerAddCmd.Flags().Bool("test", false, "Test connectivity after adding")
	providerAddCmd.Flags().String("test-model", "", "Model to test with (default: first model)")
}

var providerRefreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "Refresh provider catalog from models.dev",
	RunE: func(cmd *cobra.Command, args []string) error {
		providerFilter, _ := cmd.Flags().GetString("provider")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		imported, err := connect.RefreshModels(providerFilter, dryRun)
		if err != nil {
			return err
		}

		if len(imported) == 0 {
			fmt.Fprintln(os.Stdout, "No providers found to import")
			return nil
		}

		if dryRun {
			fmt.Fprintf(os.Stdout, "Would import %d providers:\n\n", len(imported))
		} else {
			fmt.Fprintf(os.Stdout, "Imported %d providers:\n\n", len(imported))
		}

		for _, p := range imported {
			fmt.Fprintf(os.Stdout, "  %s (%s): %d models\n", p.Name, p.Type, p.Models)
		}

		if !dryRun {
			fmt.Fprintf(os.Stdout, "\nSaved catalog to %s\n", connect.ProvidersJSONPath())
		}
		return nil
	},
}

func init() {
	providerRefreshCmd.Flags().String("provider", "", "Comma-separated list of providers to import (default: all)")
	providerRefreshCmd.Flags().Bool("dry-run", false, "Preview without writing")
	providerListCmd.Flags().Bool("models", false, "Also list each provider's models")
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		registerProviders()

		allProviders := providers.ListProviders()
		if len(allProviders) == 0 {
			fmt.Fprintln(os.Stdout, "No providers registered.")
			fmt.Fprintf(os.Stdout, "Run 'tyci provider refresh' to fetch the models.dev catalog,\n")
			fmt.Fprintf(os.Stdout, "or 'tyci provider add <name>' to add a custom provider.\n")
			return nil
		}

		showModels, _ := cmd.Flags().GetBool("models")

		for _, p := range allProviders {
			configured := p.IsConfigured()
			if configured {
				fmt.Fprintf(os.Stdout, "✓ %s\n", p.Name())
			} else {
				fmt.Fprintf(os.Stdout, "  %s (not configured)\n", p.Name())
			}

			if showModels {
				models := p.Models()

				for _, m := range models {
					fmt.Fprintf(os.Stdout, "    %s/%s\n", p.Name(), m)
				}
				if len(models) == 0 {
					fmt.Fprintln(os.Stdout, "    (no models)")
				}
			}
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// agent
// ---------------------------------------------------------------------------

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent configurations",
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured agents",
	Run: func(cmd *cobra.Command, args []string) {
		agent.DisplayAgents()
	},
}

var agentGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Show agent configuration",
	Args:  cobra.MaximumNArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		names, _ := agent.ListAgents()
		return names, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		name := args[0]
		entry, ok := agent.GetAgentEntry(name)
		if !ok {
			return fmt.Errorf("agent %q not found", name)
		}
		if entry.Model != "" {
			fmt.Printf("%s = %s\n", name, entry.Model)
		} else {
			fmt.Printf("%s = (no model set)\n", name)
		}
		if len(entry.Fallback) > 0 {
			fmt.Printf("  fallback: %s\n", strings.Join(entry.Fallback, ", "))
		}
		return nil
	},
}

var agentSetCmd = &cobra.Command{
	Use:               "set <name> [model]",
	Short:             "Set agent model",
	Args:              cobra.MaximumNArgs(2),
	ValidArgsFunction: agentSetValidArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		if len(args) < 2 {
			return fmt.Errorf("missing model argument (format: provider/model)")
		}
		name, model := args[0], args[1]
		if !strings.Contains(model, "/") || strings.HasPrefix(model, "/") || strings.HasSuffix(model, "/") {
			return fmt.Errorf("invalid model %q (expected provider/model)", model)
		}
		if err := agent.SetAgent(name, model); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Agent %q set to %s (config: %s)\n", name, model, agent.ConfigPath())
		return nil
	},
}

var agentDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete an agent",
	Args:  cobra.MaximumNArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		names, _ := agent.ListAgents()
		return names, cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		name := args[0]
		if err := agent.DeleteAgent(name); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Agent %q deleted (config: %s)\n", name, agent.ConfigPath())
		return nil
	},
}

var agentSetFallbackCmd = &cobra.Command{
	Use:               "set-fallback <name> [model...]",
	Short:             "Set fallback models for an agent",
	Args:              cobra.MinimumNArgs(1),
	ValidArgsFunction: agentSetFallbackValidArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		name := args[0]
		models := args[1:]
		for _, m := range models {
			if !strings.Contains(m, "/") || strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
				return fmt.Errorf("invalid fallback model %q (expected provider/model)", m)
			}
		}
		if err := agent.SetFallback(name, models); err != nil {
			return err
		}
		if len(models) == 0 {
			fmt.Fprintf(os.Stderr, "Fallback models removed for agent %q (config: %s)\n", name, agent.ConfigPath())
		} else {
			fmt.Fprintf(os.Stderr, "Agent %q fallback set to [%s] (config: %s)\n", name, strings.Join(models, ", "), agent.ConfigPath())
		}
		return nil
	},
}

func init() {
	agentCmd.AddCommand(agentListCmd)
	agentCmd.AddCommand(agentGetCmd)
	agentCmd.AddCommand(agentSetCmd)
	agentCmd.AddCommand(agentDeleteCmd)
	agentCmd.AddCommand(agentSetFallbackCmd)
}

// listModels returns known models in the format provider/model.
// When toComplete is empty it returns provider prefixes (e.g. "openai/") so
// the completion list stays small and fast; otherwise it filters models by the
// typed prefix.
func listModels(toComplete string) []string {
	registerProviders()
	toComplete = strings.ToLower(toComplete)

	// Build set of providers that have auth.json entries
	authKeys, err := connect.ListKeys()
	authSet := make(map[string]bool)
	if err == nil {
		for _, k := range authKeys {
			authSet[k] = true
		}
	}

	// Empty prefix: suggest provider namespaces to keep the list short and fast.
	if toComplete == "" {
		seen := make(map[string]struct{})
		for _, p := range providers.ListProviders() {
			if !authSet[p.Name()] {
				continue
			}
			seen[p.Name()+"/"] = struct{}{}
		}
		prefixes := make([]string, 0, len(seen))
		for p := range seen {
			prefixes = append(prefixes, p)
		}
		sort.Strings(prefixes)
		return prefixes
	}

	seen := make(map[string]struct{})
	for _, p := range providers.ListProviders() {
		if !authSet[p.Name()] {
			continue
		}
		prefix := p.Name() + "/"
		for _, m := range p.Models() {
			full := prefix + m
			if strings.Contains(strings.ToLower(full), toComplete) {
				seen[full] = struct{}{}
			}
		}
	}
	models := make([]string, 0, len(seen))
	for m := range seen {
		models = append(models, m)
	}
	sort.Strings(models)
	return models
}

// listProviderNames returns all provider names known to tyci:
// registered in providers.json (models.dev), model.json (legacy/custom),
// and auth.json (providers with stored keys).
func listProviderNames() []string {
	names := make(map[string]struct{})

	if entries, err := providers.LoadProvidersJSON(connect.ProvidersJSONPath()); err == nil {
		for name := range entries {
			names[name] = struct{}{}
		}
	}
	if entries, err := providers.LoadConfig(connect.ModelJSONPath()); err == nil {
		for name := range entries {
			names[name] = struct{}{}
		}
	}
	if keys, err := connect.ListKeys(); err == nil {
		for _, name := range keys {
			names[name] = struct{}{}
		}
	}

	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// agentSetValidArgs completes positional args for `agent set <name> [model]`.
// First arg is an agent name, second is a model in `provider/model` format.
func agentSetValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	switch len(args) {
	case 0:
		names, _ := agent.ListAgents()
		return names, cobra.ShellCompDirectiveNoFileComp
	case 1:
		return listModels(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

// agentSetFallbackValidArgs completes positional args for
// `agent set-fallback <name> [model...]`. First arg is the agent name,
// every subsequent arg is a model.
func agentSetFallbackValidArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		names, _ := agent.ListAgents()
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return listModels(toComplete), cobra.ShellCompDirectiveNoFileComp
}

// ---------------------------------------------------------------------------
// provider auth
// ---------------------------------------------------------------------------

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Manage provider settings",
}

var providerAuthCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage provider API keys",
}

var providerAuthSetCmd = &cobra.Command{
	Use:   "set <provider> [<key>]",
	Short: "Set API key for a provider",
	Args:  cobra.MaximumNArgs(2),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return listProviderNames(), cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		provider := args[0]
		var key string
		if len(args) >= 2 {
			key = args[1]
			if key == "-" {
				data, err := readStdin()
				if err != nil {
					return fmt.Errorf("reading key from stdin: %w", err)
				}
				key = strings.TrimSpace(string(data))
			}
		} else {
			fmt.Fprint(os.Stderr, "Enter API key: ")
			data, err := readStdin()
			if err != nil {
				return fmt.Errorf("reading key: %w", err)
			}
			key = strings.TrimSpace(string(data))
		}
		if key == "" {
			return fmt.Errorf("API key cannot be empty")
		}
		// Resolve "$ENV_VAR" references so that single-quoted or
		// escaped values like '$FOO' are not stored verbatim and used
		// as bearer tokens (see github issue: "$FOO" sent as token ->
		// 401 -> fallback).
		if connect.LooksLikeEnvRef(key) {
			resolved := connect.ResolveToken(key)
			if resolved == "" {
				return fmt.Errorf("%s is set to %q but env var %s is empty or unset", provider, key, strings.TrimPrefix(key, "$"))
			}
			fmt.Fprintf(os.Stderr, "Resolved %s -> %s\n", key, connect.MaskKey(resolved))
			key = resolved
		}
		if err := connect.SetKey(provider, key); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Saved key for provider %q in %s\n", provider, connect.AuthPath())
		return nil
	},
}

var providerAuthGetCmd = &cobra.Command{
	Use:   "get <provider>",
	Short: "Get stored API key for a provider",
	Args:  cobra.MaximumNArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return listProviderNames(), cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		provider := args[0]
		key, ok, err := connect.GetKey(provider)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no key found for provider %q", provider)
		}
		fmt.Fprintln(os.Stdout, connect.MaskKey(key))
		return nil
	},
}

var providerAuthListCmd = &cobra.Command{
	Use:   "list",
	Short: "List providers with stored keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		keys, err := connect.ListKeys()
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			fmt.Fprintln(os.Stdout, "No API keys configured in auth.json")
			return nil
		}
		fmt.Fprintln(os.Stdout, "Configured providers:")
		for _, p := range keys {
			key, ok, _ := connect.GetKey(p)
			masked := ""
			if ok {
				masked = connect.MaskKey(key)
			}
			fmt.Fprintf(os.Stdout, "  %s = %s\n", p, masked)
		}
		return nil
	},
}

var providerAuthRmCmd = &cobra.Command{
	Use:   "rm <provider>",
	Short: "Remove stored API key for a provider",
	Args:  cobra.MaximumNArgs(1),
	ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return listProviderNames(), cobra.ShellCompDirectiveNoFileComp
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		provider := args[0]
		if err := connect.RemoveKey(provider); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Removed key for provider %q\n", provider)
		return nil
	},
}

func init() {
	providerAuthCmd.AddCommand(providerAuthSetCmd)
	providerAuthCmd.AddCommand(providerAuthGetCmd)
	providerAuthCmd.AddCommand(providerAuthListCmd)
	providerAuthCmd.AddCommand(providerAuthRmCmd)
	providerCmd.AddCommand(providerAuthCmd)
	providerCmd.AddCommand(providerAddCmd)
	providerCmd.AddCommand(providerRefreshCmd)
	providerCmd.AddCommand(providerListCmd)
}

// ---------------------------------------------------------------------------
// completion
// ---------------------------------------------------------------------------

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion script",
	Long: `Generate shell completion scripts for tyci.

To load completions in the current shell session:

  Bash:
    source <(tyci completion bash)

  Zsh:
    source <(tyci completion zsh)

  Fish:
    tyci completion fish | source

  PowerShell:
    tyci completion powershell | Out-String | Invoke-Expression

To make completions persistent across new sessions:

  Bash on Linux:
    tyci completion bash > ~/.bash_completion.d/tyci
    # or append to ~/.bashrc:
    echo 'source <(tyci completion bash)' >> ~/.bashrc

  Bash on macOS:
    tyci completion bash > /usr/local/etc/bash_completion.d/tyci
    # or append to ~/.bash_profile:
    echo 'source <(tyci completion bash)' >> ~/.bash_profile

  Zsh:
    tyci completion zsh > "${fpath[1]}/_tyci"
    # or append to ~/.zshrc:
    echo 'source <(tyci completion zsh)' >> ~/.zshrc
    # Make sure compinit is enabled:
    autoload -Uz compinit && compinit

  Fish:
    tyci completion fish > ~/.config/fish/completions/tyci.fish

  PowerShell:
    tyci completion powershell | Out-String |
      Invoke-Expression -Command (Get-Clipboard)  # one-off
    # Or add to your profile:
    Add-Content -Path $PROFILE -Value 'tyci completion powershell | Out-String | Invoke-Expression'
`,
	Args: cobra.MaximumNArgs(1),
	Example: `  # Bash (Linux)
  source <(tyci completion bash)

  # Zsh
  source <(tyci completion zsh)

  # Fish
  tyci completion fish | source

  # PowerShell
  tyci completion powershell | Out-String | Invoke-Expression`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			_ = cmd.Help()
			return nil
		}
		shell := args[0]
		switch shell {
		case "bash":
			return cmd.Root().GenBashCompletionV2(os.Stdout, true)
		case "zsh":
			return cmd.Root().GenZshCompletion(os.Stdout)
		case "fish":
			return cmd.Root().GenFishCompletion(os.Stdout, true)
		case "powershell":
			return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
		default:
			return fmt.Errorf("unknown shell %q", shell)
		}
	},
}
