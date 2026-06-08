package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/internal/connect"
	"github.com/decodo/tyci-agent/internal/debug"
	"github.com/decodo/tyci-agent/internal/readline"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/session"
	"github.com/decodo/tyci-agent/stream"
	"github.com/decodo/tyci-agent/tools"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		runAgentSubcommand(os.Args[2:])
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "connect" {
		fs := flag.NewFlagSet("connect", flag.ExitOnError)
		name := fs.String("name", "", "Provider name")
		apiType := fs.String("api", "openai", "API type (openai, anthropic, gemini, responses)")
		url := fs.String("url", "", "API base URL")
		token := fs.String("token", "", "API key or $ENV_VAR reference")
		fs.Parse(os.Args[2:])
		if err := connect.Run(*name, *apiType, *url, *token); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
		fmt.Fprintf(os.Stdout, "Usage: tyci-agent [--debug] [--no-debug] [--model provider/model] [--max-retries N] [--max-iterations N] [--history-file <path>] [--mode minimal|normal|interactive|tui] (--prompt <prompt> | --interactive)\n\n")
		fmt.Fprintf(os.Stdout, "Available models:\n")
		for _, p := range providers.ListProviders() {
			for _, m := range p.Models() {
				fmt.Fprintf(os.Stdout, "  %s/%s\n", p.Name(), m)
			}
		}
		fmt.Fprintf(os.Stdout, "\nFree models:\n")
		for _, p := range providers.ListProviders() {
			for _, m := range p.FreeModels() {
				fmt.Fprintf(os.Stdout, "  %s/%s (free)\n", p.Name(), m)
			}
		}
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
	tools.SetProvider(provider)
	tools.SetCurrentModel(modelName)

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

func runAgentSubcommand(args []string) {
	if len(args) == 0 {
		// Default: list agents
		agent.DisplayAgents()
		return
	}

	cmd := args[0]
	switch cmd {
	case "list":
		agent.DisplayAgents()

	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent get <name>")
			os.Exit(1)
		}
		name := args[1]
		entry, ok := agent.GetAgentEntry(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "Agent %q not found\n", name)
			os.Exit(1)
		}
		if entry.Model != "" {
			fmt.Printf("%s = %s\n", name, entry.Model)
		} else {
			fmt.Printf("%s = (no model set)\n", name)
		}
		if len(entry.Fallback) > 0 {
			fmt.Printf("  fallback: %s\n", strings.Join(entry.Fallback, ", "))
		}

	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent set <name> --model=\"provider/model\"")
			os.Exit(1)
		}
		name := args[1]
		// Parse --model flag from remaining args
		rest := args[2:]
		model := ""
		for i, a := range rest {
			if a == "--model" || a == "-m" {
				if i+1 < len(rest) {
					model = rest[i+1]
				}
			} else if strings.HasPrefix(a, "--model=") {
				model = strings.TrimPrefix(a, "--model=")
			} else if strings.HasPrefix(a, "-m=") {
				model = strings.TrimPrefix(a, "-m=")
			}
		}
		if model == "" {
			fmt.Fprintln(os.Stderr, "Error: --model is required (format: provider/model)")
			os.Exit(1)
		}
		if err := agent.SetAgent(name, model); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Agent %q set to %s (config: %s)\n", name, model, agent.ConfigPath())

	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent delete <name>")
			os.Exit(1)
		}
		name := args[1]
		if err := agent.DeleteAgent(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Agent %q deleted (config: %s)\n", name, agent.ConfigPath())

	case "set-fallback":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent set-fallback <name> --model <fallback1> [--model <fallback2> ...]")
			os.Exit(1)
		}
		name := args[1]
		rest := args[2:]

		// Parse multiple --model flags
		var fallbacks []string
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			if a == "--model" || a == "-m" {
				if i+1 < len(rest) {
					fallbacks = append(fallbacks, rest[i+1])
					i++
				}
			} else if strings.HasPrefix(a, "--model=") {
				fallbacks = append(fallbacks, strings.TrimPrefix(a, "--model="))
			} else if strings.HasPrefix(a, "-m=") {
				fallbacks = append(fallbacks, strings.TrimPrefix(a, "-m="))
			}
		}

		if err := agent.SetFallback(name, fallbacks); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(fallbacks) == 0 {
			fmt.Fprintf(os.Stderr, "Fallback models removed for agent %q (config: %s)\n", name, agent.ConfigPath())
		} else {
			fmt.Fprintf(os.Stderr, "Agent %q fallback set to [%s] (config: %s)\n", name, strings.Join(fallbacks, ", "), agent.ConfigPath())
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown agent subcommand: %q\n", cmd)
		fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent [list|get|set|delete|set-fallback]")
		os.Exit(1)
	}
}

func watchESC(cancel context.CancelFunc) func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}

	oldState, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}

	// Set raw mode (non-canonical, no echo, etc.)
	_, err = term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}

	// Tweak: keep ISIG (for Ctrl+C signals) and OPOST (output processing),
	// set VMIN=0 VTIME=1 so read() returns every 100ms instead of blocking forever.
	if err := applyTerminalTweaks(fd); err != nil {
		term.Restore(fd, oldState)
		return func() {}
	}

	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				// timeout or error — check if we should stop before retrying
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			if buf[0] == 0x1b { // ESC
				cancel()
				return
			}
			// Any other key is discarded
		}
	}()

	return func() {
		close(stop) // signal goroutine to stop
		term.Restore(fd, oldState)
		// Don't wait for goroutine — it will exit on next timeout or stop signal
	}
}

type toolsAdapter struct{}

func (toolsAdapter) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return "", fmt.Errorf("%s", res.Error)
}

var fallbackScanner *bufio.Scanner

func simplePrompt(prompt string) (string, error) {
	if fallbackScanner == nil {
		fallbackScanner = bufio.NewScanner(os.Stdin)
	}
	fmt.Fprint(os.Stdout, prompt)
	if !fallbackScanner.Scan() {
		return "", readline.ErrEOF
	}
	return fallbackScanner.Text(), fallbackScanner.Err()
}

// replaySessionToDisplay reads a JSONL session file and replays all events
// visually through the display, showing thinking, text, tool calls, results
// and usage as they happened during the original run.
func replaySessionToDisplay(disp display.Display, sessionPath string) {
	f, err := os.Open(sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot replay session: %v\n", err)
		return
	}
	defer f.Close()

	disp.ToolBlock("📋 Session history")
	disp.End()

	type pendingTool struct {
		id   string
		name string
	}
	var pendingTools []pendingTool

	var lastUsage stream.Usage
	var totalUsage stream.Usage
	hasUsage := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		evType, _ := raw["type"].(string)
		if evType == "session" || evType == "session_end" {
			continue
		}

		msgRaw, ok := raw["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgRaw["role"].(string)
		content, _ := msgRaw["content"].([]any)

		switch role {
		case "user":
			// Show user prompt
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if txt, _ := b["text"].(string); txt != "" {
					disp.Text("User: " + txt)
				}
			}

		case "assistant":
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				bType, _ := b["type"].(string)
				switch bType {
				case "thinking":
					if txt, _ := b["thinking"].(string); txt != "" {
						disp.Thinking(txt)
					}
				case "text":
					if txt, _ := b["text"].(string); txt != "" {
						disp.Text(txt)
					}
				case "toolCall":
					id, _ := b["id"].(string)
					name, _ := b["name"].(string)
					disp.ToolCallStart(name)
					if args, ok := b["arguments"]; ok {
						switch v := args.(type) {
						case string:
							if v != "" {
								disp.ToolCallDelta(v)
							}
						case map[string]any, []any:
							if data, err := json.Marshal(v); err == nil {
								disp.ToolCallDelta(string(data))
							}
						}
					}
					pendingTools = append(pendingTools, pendingTool{id: id, name: name})
				}
			}

			// Track usage
			if u, ok := raw["usage"].(map[string]any); ok {
				lastUsage = parseUsageFromMap(u)
				totalUsage.Input += lastUsage.Input
				totalUsage.Output += lastUsage.Output
				totalUsage.Reasoning += lastUsage.Reasoning
				hasUsage = true
			}

		case "toolResult", "tool":
			for _, block := range content {
				b, ok := block.(map[string]any)
				if !ok {
					continue
				}
				bType, _ := b["type"].(string)
				if bType != "text" {
					continue
				}
				txt, _ := b["text"].(string)
				toolName, _ := b["toolName"].(string)
				toolCallID, _ := b["toolCallId"].(string)

				// Match by toolCallId or name
				matched := false
				for i, pt := range pendingTools {
					if pt.id == toolCallID || pt.name == toolName {
						disp.ToolCallEnd(pt.name, txt)
						pendingTools = append(pendingTools[:i], pendingTools[i+1:]...)
						matched = true
						break
					}
				}
				if !matched && toolName != "" {
					disp.ToolCallEnd(toolName, txt)
				} else if !matched && len(pendingTools) > 0 {
					pt := pendingTools[0]
					disp.ToolCallEnd(pt.name, txt)
					pendingTools = pendingTools[1:]
				}
			}
		}
	}

	if hasUsage {
		disp.Summary(totalUsage, stream.Stats{})
	}

	// Separator before new output
	disp.End()
	disp.ToolBlock("▶️ Continuing from session end")
	disp.End()

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session replay error: %v\n", err)
	}
}

// parseUsageFromMap extracts stream.Usage from a JSON map.
func parseUsageFromMap(u map[string]any) stream.Usage {
	var us stream.Usage
	if v, ok := u["input"].(float64); ok {
		us.Input = int(v)
	}
	if v, ok := u["output"].(float64); ok {
		us.Output = int(v)
	}
	if v, ok := u["reasoning"].(float64); ok {
		us.Reasoning = int(v)
	}
	if v, ok := u["cacheRead"].(float64); ok {
		us.CacheRead = int(v)
	}
	if v, ok := u["cacheWrite"].(float64); ok {
		us.CacheWrite = int(v)
	}
	return us
}
