package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/internal/debug"
	"github.com/decodo/tyci-agent/internal/readline"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/session"
	"github.com/decodo/tyci-agent/tools"
)

// agentRunner implements tools.SubAgentRunner by wrapping agent.Run.
type agentRunner struct{}

func (r *agentRunner) RunTask(ctx context.Context, task string, model string, temperature float64) (string, error) {
	// Resolve provider and model
	prov, mName, ok := providers.FindModel(model)
	if !ok {
		// Fallback to context values
		prov = providers.ProviderFromContext(ctx)
		mName = providers.ModelFromContext(ctx)
	}
	if prov == nil {
		return "", fmt.Errorf("no provider available for model %q", model)
	}
	if mName == "" {
		return "", fmt.Errorf("no model specified")
	}

	// Create collector to capture output
	c := &collector{}
	msgs := []providers.RichMessage{
		{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: task}},
		},
	}

	cfg := agent.Config{
		Model:         mName,
		System:        providers.BuildSystemPrompt(),
		MaxRetries:    1,
		MaxIterations: 10,
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        tools.GetSubagentToolsSchemaJSON(),
	}

	_, err := agent.Run(ctx, prov, c, &msgs, cfg)
	if err != nil {
		return "", err
	}
	return c.text.String(), nil
}

func (r *agentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, temperature float64, system string) (string, error) {
	// Resolve provider and model
	prov, mName, ok := providers.FindModel(model)
	if !ok {
		// Fallback to context values
		prov = providers.ProviderFromContext(ctx)
		mName = providers.ModelFromContext(ctx)
	}
	if prov == nil {
		return "", fmt.Errorf("no provider available for model %q", model)
	}
	if mName == "" {
		return "", fmt.Errorf("no model specified")
	}

	// Create collector to capture output
	c := &collector{}
	msgs := []providers.RichMessage{
		{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: task}},
		},
	}

	cfg := agent.Config{
		Model:         mName,
		System:        system,
		MaxRetries:    1,
		MaxIterations: 10,
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        tools.GetSubagentToolsSchemaJSON(),
	}

	_, err := agent.Run(ctx, prov, c, &msgs, cfg)
	if err != nil {
		return "", err
	}
	return c.text.String(), nil
}

// subagentToolRunner wraps the global tool registry so subagents can use tools.
type subagentToolRunner struct{}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "subagent" {
		return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
	}
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return res.Content, fmt.Errorf("%s", res.Error)
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		runAgentSubcommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "provider" {
		if len(os.Args) > 2 && os.Args[2] == "auth" {
			runProviderAuth(os.Args[3:])
		} else if len(os.Args) > 2 && os.Args[2] == "add" {
			runProviderAdd(os.Args[3:])
		} else if len(os.Args) > 2 && os.Args[2] == "refresh" {
			runProviderRefresh(os.Args[3:])
		} else if len(os.Args) > 2 {
			fmt.Fprintf(os.Stderr, "Unknown provider subcommand: %q\n", os.Args[2])
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider [auth|add|refresh]")
			os.Exit(1)
		} else {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider [auth|add|refresh]")
			os.Exit(1)
		}
		return
	}

	providers.RegisterProvidersFromConfig(filepath.Join(os.Getenv("HOME"), ".tyci", "model.json"))

	var interactiveFlag bool
	noDebugFlag := flag.Bool("no-debug", false, "Disable API request/response debug logging")
	debugFlag := flag.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := flag.String("model", "", "Model to use (format: provider/model)")
	agentFlag := flag.String("agent", "", "Agent name to use for default model (from agents config)")
	promptFlag := flag.String("prompt", "", "Prompt for response")
	maxRetriesFlag := flag.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	maxIterationsFlag := flag.Int("max-iterations", -1, "Max tool-call iterations (-1 = unlimited)")
	historyFileFlag := flag.String("history-file", "", "Path to history file (default: ~/.tyci/history)")
	modeFlag := flag.String("mode", "interactive", "Display mode: minimal, normal, interactive, tui")
	sessionFlag := flag.String("session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	noSessionFlag := flag.Bool("no-session", false, "Disable session persistence")

	flag.Usage = func() {
		_, _ = fmt.Fprintf(os.Stdout, "Usage: tyci-agent [flags] (--prompt <prompt>)\n")
		_, _ = fmt.Fprintf(os.Stdout, "       tyci-agent agent [list|get|set|delete|set-fallback]\n")
		_, _ = fmt.Fprintf(os.Stdout, "       tyci-agent provider add <name> --url <url> --token <key>\n")
		_, _ = fmt.Fprintf(os.Stdout, "       tyci-agent provider refresh [--provider p1,p2] [--dry-run]\n")
		_, _ = fmt.Fprintf(os.Stdout, "       tyci-agent provider auth [set|get|list|rm]\n\n")
		_, _ = fmt.Fprintf(os.Stdout, "Subcommands:\n")
		_, _ = fmt.Fprintf(os.Stdout, "  agent           Manage agent configurations (model assignments)\n")
		_, _ = fmt.Fprintf(os.Stdout, "  provider add    Add a provider with auth and connectivity check\n")
		_, _ = fmt.Fprintf(os.Stdout, "  provider refresh Import models from models.dev\n")
		_, _ = fmt.Fprintf(os.Stdout, "  provider auth   Manage API keys in ~/.tyci/auth.json\n\n")
		_, _ = fmt.Fprintf(os.Stdout, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	var historyFile string
	if *historyFileFlag != "" {
		historyFile = *historyFileFlag
	} else {
		var err error
		historyFile, err = readline.DefaultHistoryFile()
		if err != nil {
			historyFile = ""
		}
	}

	providers.DefaultRetryConfig = api.RetryConfig{MaxRetries: *maxRetriesFlag, BaseBackoff: 4, MaxBackoff: 128}

	// If neither --prompt nor --interactive mode given, or --prompt is empty, just exit cleanly
	if *modeFlag != "interactive" && *modeFlag != "tui" && *promptFlag == "" {
		return
	}

	model := *modelFlag
	if model == "" {
		model = agent.ResolveModel("", *agentFlag)
	}
	if model == "" {
		fmt.Fprintf(os.Stderr, "Error: no model specified. Use --model, --agent, or configure a default agent.\n")
		os.Exit(1)
	}

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: model %q not found\n", model)
		os.Exit(1)
	}

	// Resolve fallback models for the agent
	var fallbackModels []string
	agentName := *agentFlag
	if agentName == "" {
		agentName = "default"
	}
	if fb := agent.GetFallbackModels(agentName); len(fb) > 0 {
		fallbackModels = fb
	}

	var ctx context.Context
	if !*noDebugFlag {
		dl, err := debug.Init()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: debug log: %v\n", err)
			ctx = context.Background()
		} else {
			defer dl.Close()
			ctx = debug.NewContext(context.Background(), dl)
		}
	} else {
		ctx = context.Background()
	}

	var disp display.Display
	mode := *modeFlag
	switch mode {
	case "minimal":
		disp = display.NewMinimal()
	case "normal":
		disp = display.NewTerminal()
	case "interactive":
		interactiveFlag = true
		disp = display.NewTerminal()
	case "tui":
		// Build list of all available models for Tab switching and model picker
		var allModels []string
		var allProviderModels []display.ProviderModels
		for _, p := range providers.ListProviders() {
			pm := display.ProviderModels{Name: p.Name()}
			for _, m := range p.Models() {
				allModels = append(allModels, p.Name()+"/"+m)
				pm.Models = append(pm.Models, m)
			}
			for _, m := range p.FreeModels() {
				allModels = append(allModels, p.Name()+"/"+m)
				pm.Models = append(pm.Models, m)
			}
			if len(pm.Models) > 0 {
				allProviderModels = append(allProviderModels, pm)
			}
		}
		tuiDisp := display.NewTUI(model, historyFile, allModels, allProviderModels)
		disp = tuiDisp
		interactiveFlag = true
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q (expected minimal, normal, interactive, or tui)\n", mode)
		os.Exit(1)
	}

	cfg := agent.Config{
		Model:          modelName,
		System:         providers.BuildSystemPrompt(),
		MaxRetries:     *maxRetriesFlag,
		MaxIterations:  *maxIterationsFlag,
		Debug:          *debugFlag,
		Tools:          toolsAdapter{},
		Schema:         tools.GetToolsSchemaJSON(),
		ProviderName:   provider.Name(),
		FallbackModels: fallbackModels,
	}
	tools.SetSubAgentRunner(&agentRunner{})

	// Session setup
	wd, _ := os.Getwd()
	var sess *session.Session
	var sessionPath string
	if !*noSessionFlag {
		sessionPath = *sessionFlag
		if sessionPath == "" {
			var err error
			sessionPath, err = session.DefaultPath(wd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: cannot determine session path: %v\n", err)
			}
		}
		if sessionPath != "" {
			var err error
			sess, err = session.Open(sessionPath, wd, modelName, provider.Name())
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: session: %v (continuing without session)\n", err)
				sess = nil
				sessionPath = ""
			}
		}
	}
	cfg.Session = sess

	if interactiveFlag {
		if tuiDisp, ok := disp.(*display.TUI); ok {
			runTUI(provider, modelName, tuiDisp, cfg, ctx, sessionPath)
		} else {
			runInteractive(provider, modelName, disp, historyFile, cfg, ctx, sessionPath)
		}
		return
	}

	runPrompt(provider, disp, *promptFlag, cfg, ctx, sess, sessionPath)
}









