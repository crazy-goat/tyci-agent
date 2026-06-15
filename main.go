package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/tools"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "agent":
			runAgentSubcommand(os.Args[2:])
			return
		case "provider":
			handleProviderSubcommand(os.Args[2:])
			return
		case "run", "console", "tui":
			if err := runSubcommand(os.Args[1], os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			return
		case "-h", "--help", "help":
			printUsage(os.Stdout)
			return
		}
	}

	if hasFlags(os.Args[1:]) {
		fmt.Fprintln(os.Stderr, "Error: a subcommand is required (run, console, tui, agent, provider).")
		printUsage(os.Stderr)
		os.Exit(1)
	}
	printUsage(os.Stdout)
}

func handleProviderSubcommand(args []string) {
	if len(args) > 0 && args[0] == "auth" {
		runProviderAuth(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "add" {
		runProviderAdd(args[1:])
		return
	}
	if len(args) > 0 && args[0] == "refresh" {
		runProviderRefresh(args[1:])
		return
	}
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "Unknown provider subcommand: %q\n", args[0])
		fmt.Fprintln(os.Stderr, "Usage: tyci provider [auth|add|refresh]")
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "Usage: tyci provider [auth|add|refresh]")
	os.Exit(1)
}

func hasFlags(args []string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "Usage: tyci <subcommand> [flags]\n\n")
	fmt.Fprintln(w, "Subcommands:")
	fmt.Fprintln(w, "  run              One-shot run with a single --prompt (minimal display)")
	fmt.Fprintln(w, "  console          Interactive REPL with readline, history, slash commands")
	fmt.Fprintln(w, "  tui              Bubble Tea TUI with model picker, split-pane, mouse support")
	fmt.Fprintln(w, "  agent            Manage agent configurations (list|get|set|delete|set-fallback)")
	fmt.Fprintln(w, "  provider add     Add a provider with auth and connectivity check")
	fmt.Fprintln(w, "  provider refresh Import models from models.dev")
	fmt.Fprintln(w, "  provider auth    Manage API keys in ~/.tyci/auth.json")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Common flags (run, console, tui):")
	fmt.Fprintln(w, "  --model <provider/model>   Model to use")
	fmt.Fprintln(w, "  --agent <name>             Use agent preset (from ~/.tyci/agents.json)")
	fmt.Fprintln(w, "  --prompt <text>            Prompt text (required for `run`)")
	fmt.Fprintln(w, "  --max-retries <n>          Max retries on transient errors (default 5)")
	fmt.Fprintln(w, "  --max-iterations <n>       Max tool-call iterations (-1 = unlimited)")
	fmt.Fprintln(w, "  --debug                    Show HTTP request/response data")
	fmt.Fprintln(w, "  --no-debug                 Disable debug logging")
	fmt.Fprintln(w, "  --session <path>           Session file path")
	fmt.Fprintln(w, "  --no-session               Disable session persistence")
	fmt.Fprintln(w, "  --history-file <path>      Path to readline history file")
}

func runSubcommand(kind string, args []string) error {
	providers.RegisterProvidersFromConfig(filepath.Join(os.Getenv("HOME"), ".tyci", "model.json"))

	fs := flag.NewFlagSet("tyci "+kind, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noDebugFlag := fs.Bool("no-debug", false, "Disable API request/response debug logging")
	debugFlag := fs.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := fs.String("model", "", "Model to use (format: provider/model)")
	agentFlag := fs.String("agent", "", "Agent name to use for default model (from agents config)")
	promptFlag := fs.String("prompt", "", "Prompt for response (required for `run`)")
	maxRetriesFlag := fs.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	maxIterationsFlag := fs.Int("max-iterations", -1, "Max tool-call iterations (-1 = unlimited)")
	historyFileFlag := fs.String("history-file", "", "Path to history file (default: ~/.tyci/history)")
	sessionFlag := fs.String("session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	noSessionFlag := fs.Bool("no-session", false, "Disable session persistence")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if kind == "run" && *promptFlag == "" {
		return fmt.Errorf("Error: `tyci run` requires --prompt")
	}

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

	model := *modelFlag
	if model == "" {
		model = agent.ResolveModel("", *agentFlag)
	}
	if model == "" {
		return fmt.Errorf("Error: no model specified. Use --model, --agent, or configure a default agent.")
	}

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		return fmt.Errorf("Error: model %q not found", model)
	}

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
	interactiveFlag := false
	switch kind {
	case "run":
		disp = display.NewMinimal()
	case "console":
		interactiveFlag = true
		disp = display.NewTerminal()
	case "tui":
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
		return nil
	}

	runPrompt(provider, disp, *promptFlag, cfg, ctx, sess, sessionPath)
	return nil
}









