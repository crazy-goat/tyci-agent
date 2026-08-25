package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/internal/hooks"
	"github.com/decodo/tyci/internal/pricing"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/internal/skills"
	"github.com/decodo/tyci/internal/trust"
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
	rootCmd.PersistentFlags().Int("max-tokens", 0, "Max tokens in one model reply (0 = value from ~/.tyci/config.json, else the provider default)")
	rootCmd.PersistentFlags().String("history-file", "", "Path to history file (default: ~/.tyci/history)")
	rootCmd.PersistentFlags().String("session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	rootCmd.PersistentFlags().Bool("no-session", false, "Disable session persistence")
	rootCmd.PersistentFlags().Bool("debug", false, "Show HTTP request/response data")
	rootCmd.PersistentFlags().Bool("no-debug", false, "Disable API request/response debug logging")
	rootCmd.PersistentFlags().Bool("no-mcp", false, "Don't connect configured MCP servers (~/.tyci/mcp.json)")

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

// registerProviders reads the global provider catalogs and, for model.json
// (TODO.md item 22), a project-local <wd>/.tyci/model.json too — unioned
// with the global one, local winning on a (group, model name) collision
// (see providers.RegisterProvidersFromConfigMerged). Self-contained on
// os.Getwd() rather than threaded a wd, the same posture as
// agent.LoadTyciConfig and agent.LoadAgents.
func registerProviders() {
	if err := connect.EnsureProvidersJSON(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: providers.json: %v\n", err)
	}
	providers.RegisterProvidersFromProvidersJSON(connect.ProvidersJSONPath())
	providers.RegisterProvidersFromConfigMerged(connect.ModelJSONPath(), localModelJSONPath())
}

// localModelJSONPath returns <cwd>/.tyci/model.json, or "" when cwd cannot
// be determined.
func localModelJSONPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(wd, ".tyci", "model.json")
}

// initCommon wires up everything a command needs to run the agent loop:
// provider/model resolution, debug logging, session, history file, and the
// tool schema handed to the model.
//
// connectMCP decides whether this invocation connects configured MCP
// servers (tools.InitMCP) before building the schema, subject to the
// --no-mcp flag (which always wins, whatever connectMCP says — see below).
// console and tui pass true. run also passes true — a scheduled `tyci cron`
// job shells out to `tyci run` (internal/cron/run.go), so leaving run's
// default at false would silently give every cron job a different, smaller
// tool set than an interactive session gets, which is a hard failure mode
// to debug precisely because the same prompt behaves differently depending
// on how it was started. The cost is bounded: connects happen concurrently
// and mcpConnectTimeout caps the whole batch at 5s (tools/mcp.go), which is
// judged an acceptable one-time cost even for a scripted/piped invocation.
// --no-mcp is the opt-out for whichever of these one-shot callers wants it.
//
// The returned shutdown func closes any connected MCP servers and must be
// deferred by the caller so a stdio server's child process never outlives
// this process; it is a no-op when MCP was never connected.
// interactive tells initCommon whether a human is present in the
// conversation at all — true for console and tui, false for run (and cron,
// which shells out to `tyci run`: see this func's doc comment on
// connectMCP). Threaded onto cfg.Interactive, which buildJobReminder
// (agent/agent.go) reads to decide whether it is honest to tell the model
// a user will answer a blocked job.
func initCommon(cmd *cobra.Command, connectMCP bool, interactive bool) (providers.Provider, string, agent.Config, context.Context, *session.Session, string, string, *debug.Logger, func(), error) {
	registerProviders()

	// Trust decision (item 23, internal/trust): does this project get to run
	// its own project-local hooks/Lua tools/mcp.json? Resolved before any of
	// that project-local content is loaded below, and before the TUI (if
	// this is a `tui` invocation) takes the terminal — interactive is only
	// ever true for console/tui, both of which call initCommon before
	// touching the screen (see tuiCmd/consoleCmd), so trust.Decide's blocking
	// stdio prompt is safe to run here. `run` (and therefore cron, which
	// shells out to `tyci run`) passes interactive=false, so an unknown
	// project there defaults to untrusted without ever blocking.
	wd, _ := os.Getwd()
	projectRoot, _ := session.ProjectKey(wd)
	trusted, _, err := trust.Decide(projectRoot, interactive, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: trust: %v\n", err)
	}
	if !trusted {
		fmt.Fprintln(os.Stderr,
			"tyci: this project is not trusted — project-local hooks (.tyci/hooks.json) "+
				"and Lua tools (.tyci/tools/) are skipped this session. Global ~/.tyci/ "+
				"content still loads as usual. Run tyci in an interactive mode (console/tui) "+
				"in this directory to be asked, or edit ~/.tyci/trust.json directly.")
	}

	// Hook config: global always, project-local only once this project is
	// trusted (see trust.Decide above). hooks.DefaultPaths puts the global
	// path first and the project path second, so slicing to [:1] keeps only
	// the global one for an untrusted project.
	hookPaths := hooks.DefaultPaths(wd)
	if !trusted {
		hookPaths = hookPaths[:1]
	}
	// Hook config problems are reported here, before any display owns the
	// screen, because a hook that failed to load is a silent loss of a
	// protection the user thinks they have.
	for _, err := range hooks.Load(hookPaths...) {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}

	// Project-local Lua tools (./.tyci/tools) — same trust gate. The global
	// ones (~/.tyci/tools) are already loaded unconditionally by the tools
	// package's own init() (see tools.LoadAndRegisterLuaTools).
	if trusted {
		tools.LoadAndRegisterLocalLuaTools(filepath.Join(wd, ".tyci", "tools"))
	}

	// Project-local cron.json (TODO.md item 22) — same trust gate: a
	// scheduled job is a whole unattended agent turn, the same shape of
	// risk as hooks.json and .tyci/tools. Recorded here (always, not just
	// when trusted — an untrusted decision must overwrite whatever a
	// previous call in this process set, the same reset-on-every-call shape
	// SetBackgroundBashEnabled/SetJobStarter use) rather than decided again
	// inside tools/cron.go.
	if trusted {
		tools.SetLocalCronDir(filepath.Join(wd, ".tyci"))
	} else {
		tools.SetLocalCronDir("")
	}

	// Project-local mcp.json (TODO.md item 22) is gated the same way, down
	// at the tools.InitMCP call below: a server definition there can launch
	// an arbitrary binary, exactly the shape of trust hooks.json and
	// .tyci/tools already require. `trusted` is threaded through rather
	// than decided again there.

	maxRetries, _ := cmd.Flags().GetInt("max-retries")
	providers.DefaultRetryConfig = api.RetryConfig{MaxRetries: maxRetries, BaseBackoff: 4, MaxBackoff: 128}

	model, _ := cmd.Flags().GetString("model")
	agentName, _ := cmd.Flags().GetString("agent")
	explicitModel := model != ""
	explicitAgent := agentName != ""
	if model == "" {
		model = agent.ResolveModel("", agentName)
	}
	if model == "" {
		return nil, "", agent.Config{}, nil, nil, "", "", nil, nil, fmt.Errorf("no model specified. Use --model, --agent, or configure a default agent")
	}

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		return nil, "", agent.Config{}, nil, nil, "", "", nil, nil, fmt.Errorf("model %q not found", model)
	}
	// A catalog written by an older tyci build had its prices and context
	// limits silently stripped on every refresh (see doc comment on
	// connect.ModelsDevModel), and a hand-added provider can lack pricing
	// from the start. Warn about the provider actually in use — the only
	// moment the message is true and actionable — rather than the whole
	// catalog, which one priced provider among many unpriced ones would
	// otherwise silence forever.
	if pricing.ProviderNeedsPrices(provider.Name()) {
		fmt.Fprintln(os.Stderr, "Warning: no prices for provider "+provider.Name()+" in providers.json — run `tyci provider refresh` to fix it.")
	}

	if agentName == "" {
		agentName = "default"
	}
	var fallbacks []connector.ModelClient
	// An explicit --model must not silently inherit the default agent's
	// fallback list. Otherwise selecting an expensive model still allows a
	// stale/default fallback (for example another paid model) to run after a
	// transient setup error. An explicitly supplied --agent opts into that
	// agent's fallback policy even when --model also overrides its primary.
	if !explicitModel || explicitAgent {
		if fb := agent.GetFallbackModels(agentName); len(fb) > 0 {
			fallbacks = resolveFallbacks(fb)
		}
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

	// shutdown is what the caller defers. Connecting MCP servers ties their
	// child processes' lifetime to ctx (see mcp.ConnectAllTimeout's stdio
	// path, exec.CommandContext) — canceling it is the backstop that kills
	// even a server whose handshake never completed and so was never
	// registered for tools.ShutdownMCP to close gracefully. --no-mcp always
	// wins over connectMCP: it exists specifically so a caller that would
	// otherwise connect (run, for cron's sake — see this func's doc
	// comment) can still opt out. When MCP ends up not connecting at all,
	// ctx is never wrapped with a cancel: nothing was started, so there is
	// nothing to cancel or close, and shutdown is a plain no-op.
	noMCP, _ := cmd.Flags().GetBool("no-mcp")
	shutdown := func() {}
	if connectMCP && !noMCP {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		if err := tools.InitMCP(ctx, wd, trusted); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: MCP: %v\n", err)
		}
		shutdown = func() {
			tools.ShutdownMCP()
			cancel()
		}
	}

	debugFlag, _ := cmd.Flags().GetBool("debug")
	maxIterations, _ := cmd.Flags().GetInt("max-iterations")
	// The flag wins when given, otherwise the global config, otherwise 0 —
	// which each connector reads as "your default".
	maxTokens, _ := cmd.Flags().GetInt("max-tokens")
	if maxTokens <= 0 {
		maxTokens = agent.GetMaxTokens()
	}
	cfg := agent.Config{
		System:          providers.BuildSystemPrompt(),
		MaxRetries:      maxRetries,
		MaxIterations:   maxIterations,
		Debug:           debugFlag,
		Tools:           toolsAdapter{},
		Schema:          tools.GetTopLevelToolsSchemaJSON(),
		Fallbacks:       fallbacks,
		MaxTokens:       maxTokens,
		NoPromptCache:   !agent.PromptCacheEnabled(),
		PendingTodos:    tools.PendingTodos,
		ActiveSubagents: JobRegistry.HasActiveSubagents,
		PendingJobs:     JobRegistry.PendingLines,
		HasTodos:        tools.HasPendingTodos,
		ContextLimit:    pricingContextLimit(provider.Name(), modelName),
		ContextLimitFor: pricingContextLimit,
		Interactive:     interactive,
	}
	ctx = connector.WithModelClient(ctx, provider.Client(modelName))

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

	return provider, modelName, cfg, ctx, sess, sessionPath, historyFile, dl, shutdown, nil
}

func pricingContextLimit(provider, model string) int {
	_, limits := pricing.Lookup(provider, model)
	return limits.Context
}

// manualCompactSummary is used when a human types /compact directly, rather
// than the model calling the compact tool with its own summary. There is no
// model-authored summary to fall back on here, but compaction never deletes
// anything (see agent.CompactSession / session.WriteCompaction): the raw
// JSONL and the derived markdown dump both survive, so a generic marker
// costs nothing. Any text the person typed after /compact is the optional
// focus instruction, not the summary itself.
const manualCompactSummary = "Manual /compact requested by the user. Earlier turns are not repeated here — see the raw session file and its markdown dump for the full record."

// resolveFallbacksQuiet resolves each "provider/model" fallback spec to a
// connector.ModelClient and reports nothing itself: it returns the resolved
// clients alongside the specs that could not be resolved, leaving it to the
// caller to decide how (or whether) to surface those. This split exists
// because "resolve" and "report" have different constraints depending on who
// is calling: the top-level CLI setup path (resolveFallbacks below) can
// freely write to stderr before the TUI takes over the screen, but a subagent
// resolving ITS OWN fallback list runs mid-session, often under the Bubble
// Tea TUI, where an unguarded stderr write would corrupt the display (see
// agentRunner.run in main.go, which logs unresolved specs to the debug log
// instead).
func resolveFallbacksQuiet(specs []string) (clients []connector.ModelClient, unresolved []string) {
	for _, spec := range specs {
		p, m, ok := providers.FindModel(spec)
		if !ok {
			unresolved = append(unresolved, spec)
			continue
		}
		clients = append(clients, p.Client(m))
	}
	return clients, unresolved
}

// resolveFallbacks resolves each "provider/model" fallback spec to a
// connector.ModelClient at setup time — the agent no longer resolves
// fallback specs itself (see agent.Config.Fallbacks). A spec that fails to
// resolve is reported here and skipped, which is a deliberate relocation:
// agent.Run used to discover this lazily, mid-run, and report it via a
// ToolBlock on the display; now it is reported once, at startup, on stderr.
// This is the top-level-agent path only; a subagent's own fallback list goes
// through resolveFallbacksQuiet instead (see its comment for why).
func resolveFallbacks(specs []string) []connector.ModelClient {
	clients, unresolved := resolveFallbacksQuiet(specs)
	for _, spec := range unresolved {
		fmt.Fprintf(os.Stderr, "Warning: fallback model %q not found, skipping\n", spec)
	}
	return clients
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
		// connectMCP: true. See initCommon's doc comment — cron shells out
		// to `tyci run`, so this is what gives a scheduled job the same
		// tool set an interactive session gets. --no-mcp opts a particular
		// invocation out.
		// interactive: false — a one-shot run has no one present to reply at
		// all (see agent.Config.Interactive's doc comment).
		provider, modelName, cfg, ctx, _, sessionPath, _, dl, shutdown, err := initCommon(cmd, true, false)
		if err != nil {
			return err
		}
		// cleanup bundles everything that must run once, however this
		// invocation ends: the normal return below (via defer) AND the two
		// os.Exit paths inside finishPromptRun (via the cleanup param passed
		// to runPrompt) -- os.Exit terminates before any defer gets to run,
		// so runPrompt/finishPromptRun call this explicitly right before
		// each os.Exit. Without it, an errored or Ctrl-C'd `tyci run` would
		// skip tools.ShutdownMCP() (and the debug log's Close()) entirely,
		// leaking a connected MCP server's process on every failed or
		// canceled run -- cron's included, since it just shells out to
		// `tyci run`. See finishPromptRun's doc comment (prompt_finish.go).
		cleanup := func() {
			if dl != nil {
				dl.Close()
			}
			shutdown()
		}
		defer cleanup()
		// `tyci run` is a one-shot CLI invocation. Use the
		// bracket-prefix Minimal display so output is plain, one line
		// per event, and easy to grep / pipe. For the rich REPL or
		// full-screen experience, use `tyci console` or
		// `tyci tui` instead.
		disp := display.NewMinimal()
		// No resolver: a one-shot run has nowhere to type /model, so
		// SwitchModel is not reachable and does not need a catalog.
		cond := newConductor(provider, modelName, disp, cfg, sessionPath, nil)
		runPrompt(cond, disp, prompt, ctx, cleanup)
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
		// interactive: true — a human is at the readline prompt (see
		// agent.Config.Interactive's doc comment).
		provider, modelName, cfg, ctx, _, sessionPath, historyFile, dl, shutdown, err := initCommon(cmd, true, true)
		if err != nil {
			return err
		}
		defer shutdown()
		if dl != nil {
			defer dl.Close()
		}
		disp := display.NewTerminal()

		// The console can background shell commands too, but its input is a
		// blocking readline, so it cannot wake itself: a completion notice
		// waits in the queue and is delivered with the user's next message
		// (or picked up mid-turn by the drain below, if a turn is running).
		cfg.NextMessages = mergeNextMessages(cfg.NextMessages, JobNotices.Drain)
		tools.SetBackgroundBashEnabled(true)
		defer tools.KillAllBackgroundBash()
		// Saved prompts only fire while something is ticking the schedule, and
		// an interactive session is the one place where a run finishing has
		// somewhere to be reported. See tools.StartCronTicker.
		tools.StartCronTicker(ctx, time.Minute)

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
		// interactive: true — a human is at the keyboard (see
		// agent.Config.Interactive's doc comment).
		provider, modelName, cfg, ctx, _, sessionPath, historyFile, dl, shutdown, err := initCommon(cmd, true, true)
		if err != nil {
			return err
		}
		defer shutdown()
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
		skillsFound, _ := skills.ListSkillsMerged("")
		skillsCount := len(skillsFound)
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
		}, toolsCount, skillsCount, mcpCount, agent.GetSidebarVisible())

		// Persist the sidebar's visibility across restarts: NewTUI above read
		// the startup state (agent.GetSidebarVisible), this persister is
		// invoked by the TUI whenever the user opens or closes the sidebar.
		tuiDisp.SetSidebarPersister(func(visible bool) { _ = agent.SetSidebarVisible(visible) })

		// Show async subagent jobs (subagent(async: true), see tools/subagent.go)
		// in the TUI's background-jobs panel/modal (Ctrl+B).
		tuiDisp.SetJobEventBus(jobEventBus)

		// Wire the sidebar's Sessions tab (TODO item 1) to the same
		// cwd-scoped session listing bare "/resume" already uses (tui_mode.go)
		// — reusing session.ResumeEntries rather than the display package
		// importing "session" itself (same layering rule as jobs/tools).
		tuiDisp.SetSessionLister(func() []display.TuiResumeEntry {
			wd, _ := os.Getwd()
			entries, err := session.ResumeEntries(wd)
			if err != nil {
				return nil
			}
			return resumeEntriesToTUI(entries)
		})

		// Issue #88: wire the pending-message queue drain callback. The
		// TUI's NextMessages drains the channel of user lines typed
		// during the in-flight request and returns them in FIFO order;
		// the agent loop appends each as a user message and forces one
		// more runOnce so the model sees them as a single turn. Completion
		// notices are wrapped by runTUI, where they are also rendered visibly.
		cfg.NextMessages = tuiDisp.NextMessages

		// Long-running shell commands may be moved to the background in the
		// TUI: it is the one mode that both drains completion notices between
		// turns (above) and can wake itself up to act on one while idle (see
		// runTUI). Commands outlive the tool call that started them, so
		// runTUI kills any survivors on exit.
		tools.SetBackgroundBashEnabled(true)

		// Saved prompts only fire while something is ticking the schedule, and
		// an interactive session is the one place where a run finishing has
		// somewhere to be reported. See tools.StartCronTicker.
		tools.StartCronTicker(ctx, time.Minute)

		// No requireConfigured: the Tab-cycle and the picker only offer
		// providers that are already in auth.json (see authSet above), and
		// silently refusing a favorite would read as a dead key press.
		cond := newConductor(provider, modelName, tuiDisp, cfg, sessionPath, catalogResolver{})
		runTUI(cond, tuiDisp, ctx)
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

		imported, keptUnchanged, skippedZeroModels, err := connect.RefreshModels(providerFilter, dryRun)
		if err != nil {
			return err
		}

		if len(imported) == 0 {
			fmt.Fprintln(os.Stdout, "No providers found to import")
			if keptUnchanged > 0 {
				fmt.Fprintf(os.Stdout, "%d existing provider(s) left untouched\n", keptUnchanged)
			}
			if skippedZeroModels > 0 {
				fmt.Fprintf(os.Stdout, "%d provider(s) were fetched but returned no models; their cached prices may be stale\n", skippedZeroModels)
			}
			return nil
		}

		verb := "Imported"
		if dryRun {
			verb = "Would import"
		}
		fmt.Fprintf(os.Stdout, "%s %d providers:\n\n", verb, len(imported))

		for _, p := range imported {
			status := "new"
			if p.Replaced {
				status = "replacing existing"
			}
			fmt.Fprintf(os.Stdout, "  %s (%s): %d models [%s]\n", p.Name, p.Type, p.Models, status)
		}

		if keptUnchanged > 0 {
			verb = "left"
			if dryRun {
				verb = "would be left"
			}
			fmt.Fprintf(os.Stdout, "\n%d existing provider(s) %s untouched (not fetched from models.dev)\n", keptUnchanged, verb)
		}

		if skippedZeroModels > 0 {
			fmt.Fprintf(os.Stdout, "%d provider(s) were fetched but returned no models; their cached prices may be stale\n", skippedZeroModels)
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

			// A warning does not change the ✓/✗ above — see ConfigWarnings —
			// it flags a credential that IS present but looks unresolvable,
			// so the user finds out here instead of from a bare "no API key"
			// error at request time.
			for _, envVar := range p.ConfigWarnings() {
				_, _ = fmt.Fprintf(os.Stdout, "    warning: URI references $%s, but env var %s is empty or unset\n", envVar, envVar)
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

var agentSyncForce bool

var agentSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Unpack/update tyci's builtin agent definitions in ~/.tyci/agents/",
	Long: `Unpack tyci's builtin agent definitions (locator, reviewer, implementer) into
~/.tyci/agents/, and update the ones already there — but only when they are
still exactly what tyci last wrote. A file you edited, or a name you deleted
on purpose, is left alone.

This runs automatically (and silently) on every tyci startup; this command
exists to run it on demand and, unlike the automatic pass, report what it did.

--force overwrites everything unconditionally, INCLUDING your local edits to
builtin agent files and any builtin file you deleted (it comes back). Use it
to deliberately reset to tyci's stock versions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := agentdefs.GlobalDir()
		res, err := agentdefs.Sync(dir, agentSyncForce)
		if err != nil {
			return fmt.Errorf("sync agent definitions: %w", err)
		}

		printed := false
		report := func(label string, names []string) {
			if len(names) == 0 {
				return
			}
			printed = true
			fmt.Printf("%s: %s\n", label, strings.Join(names, ", "))
		}
		report("Installed", res.Installed)
		report("Updated", res.Updated)
		report("Skipped (locally modified)", res.SkippedModified)
		report("Skipped (deleted)", res.SkippedDeleted)
		if !printed {
			fmt.Printf("Everything up to date in %s\n", dir)
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
	agentSyncCmd.Flags().BoolVar(&agentSyncForce, "force", false, "overwrite all builtin agent files unconditionally, including local edits and deleted files")
	agentCmd.AddCommand(agentSyncCmd)
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
	if local := localModelJSONPath(); local != "" {
		if entries, err := providers.LoadConfig(local); err == nil {
			for name := range entries {
				names[name] = struct{}{}
			}
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
