package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

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
	historyFileFlag := flag.String("history-file", "", "Path to history file (default: ~/.tyci/history)")
	modeFlag := flag.String("mode", "interactive", "Display mode: minimal, normal, interactive, tui")
	sessionFlag := flag.String("session", "", "Session file path (default: auto-generated in ~/.tyci/sessions/)")
	noSessionFlag := flag.Bool("no-session", false, "Disable session persistence")

	flag.Usage = func() {
		fmt.Fprintf(os.Stdout, "Usage: tyci-agent [--debug] [--no-debug] [--model provider/model] [--max-retries N] [--history-file <path>] [--mode minimal|normal|interactive|tui] (--prompt <prompt> | --interactive)\n\n")
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
		tuiDisp := display.NewTUI()
		disp = tuiDisp
		interactiveFlag = true
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q (expected minimal, normal, interactive, or tui)\n", mode)
		os.Exit(1)
	}

	cfg := agent.Config{
		Model:      modelName,
		System:     providers.BuildSystemPrompt(),
		MaxRetries: *maxRetriesFlag,
		Debug:      *debugFlag,
		Tools:      toolsAdapter{},
		Schema:     tools.GetToolsSchemaJSON(),
		ProviderName: provider.Name(),
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

	// Create cancellable context for the agent run
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Handle Ctrl+C during non-interactive agent run
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		select {
		case <-sigCh:
			runCancel()
		case <-runCtx.Done():
		}
	}()

	// Also watch for ESC if stdin is a terminal
	stopESC := watchESC(runCancel)

	messages := []providers.RichMessage{{
		Role: "user",
		Content: []providers.ContentBlock{{Type: "text", Text: *promptFlag}},
	}}

	// Resume from session if applicable
	if sess != nil && sess.IsResume() {
		parsedLines := sess.Messages()
		rebuiltMsgs, err := session.RebuildMessages(parsedLines)
		if err == nil && len(rebuiltMsgs) > 0 {
			conversation := rebuiltMsgs
			fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages) from %s\n", sess.ID(), len(conversation), sessionPath)

			// Visual replay of the session history
			replaySessionToDisplay(disp, sessionPath)

			// Append current prompt as new user message
			conversation = append(conversation, providers.RichMessage{
				Role:    "user",
				Content: []providers.ContentBlock{{Type: "text", Text: *promptFlag}},
			})
			messages = conversation
		}
	}

	// Write user message to session
	if sess != nil && *promptFlag != "" {
		blocks := []session.ContentBlock{{Type: "text", Text: *promptFlag}}
		_ = sess.WriteMessage("user", blocks, nil)
	}

	usage, err := agent.Run(runCtx, provider, disp, &messages, cfg)

	stopESC()
	signal.Stop(sigCh)
	runCancel()
	<-sigDone

	status := "ok"
	exitCode := 0
	if err != nil {
		if errors.Is(err, context.Canceled) {
			disp.End()
			fmt.Fprint(os.Stdout, "\n")
			agent.WriteSessionEnd(sess, "cancelled", 130, &usage)
			if sessionPath != "" {
				fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
			}
			os.Exit(130) // standard exit code for SIGINT
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		status = "error"
		exitCode = 1
	}
	agent.WriteSessionEnd(sess, status, exitCode, &usage)

	if err != nil {
		if sessionPath != "" {
			fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
		}
		os.Exit(exitCode)
	}
	disp.End()
	fmt.Fprintln(os.Stdout)
	if sessionPath != "" {
		fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
	}
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
		model, ok := agent.GetAgent(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "Agent %q not found\n", name)
			os.Exit(1)
		}
		fmt.Printf("%s = %s\n", name, model)

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

	default:
		fmt.Fprintf(os.Stderr, "Unknown agent subcommand: %q\n", cmd)
		fmt.Fprintln(os.Stderr, "Usage: tyci-agent agent [list|get|set|delete]")
		os.Exit(1)
	}
}

func runInteractive(provider providers.Provider, modelName string, disp display.Display, historyFile string, cfg agent.Config, baseCtx context.Context, sessionPath string) {
	var conversation []providers.RichMessage
	var totalUsage stream.Usage

	// Replay session history if resuming
	if cfg.Session != nil && cfg.Session.IsResume() && sessionPath != "" {
		replaySessionToDisplay(disp, sessionPath)
		// Load conversation from session
		parsedLines := cfg.Session.Messages()
		rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
		if len(rebuiltMsgs) > 0 {
			conversation = rebuiltMsgs
			fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages)\n", cfg.Session.ID(), len(conversation))
		}
	}

	var editor *readline.LineEditor
	if historyFile != "" {
		var err error
		editor, err = readline.New(historyFile, readline.DefaultMaxEntries)
		if err != nil {
			fmt.Fprintf(os.Stdout, "Warning: cannot init readline: %v\n", err)
			editor = nil
		}
	}
	if editor != nil {
		defer editor.Close()
	}

	// Close session on exit with accumulated usage
	defer func() {
		if cfg.Session != nil {
			agent.WriteSessionEnd(cfg.Session, "ok", 0, &totalUsage)
			if sessionPath != "" {
				fmt.Fprintf(os.Stderr, "📁 Session: %s\n", sessionPath)
			}
		}
	}()

	for {
		// Per-iteration context — Ctrl+C or ESC cancels this, returning to prompt
		iterCtx, iterCancel := context.WithCancel(baseCtx)

		var line string
		var err error

		if editor != nil {
			line, err = editor.Read(iterCtx, ">>> (Alt+Enter to send) ")
		} else {
			line, err = simplePrompt(">>> ")         // Enter to send (no multiline)
		}

		if errors.Is(err, readline.ErrEOF) {
			iterCancel()
			fmt.Println("Bye!")
			return
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, readline.ErrInterrupt) {
				// Ctrl+C or ESC during input — return to prompt
				iterCancel()
				fmt.Fprint(os.Stdout, "\n")
				continue
			}
			fmt.Fprintf(os.Stdout, "Read error: %v\n", err)
			iterCancel()
			continue
		}

		line = strings.TrimSpace(line)
		if line == "/exit" {
			iterCancel()
			fmt.Println("Bye!")
			return
		}
		if line == "/new" {
			iterCancel()
			conversation = nil
			fmt.Print("\033[2J\033[H")
			continue
		}
		if line == "" {
			iterCancel()
			continue
		}

		if editor != nil {
			editor.AddHistory(line)
		}

		conversation = append(conversation, providers.RichMessage{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: line}},
		})

		// Write user message to session
		if cfg.Session != nil {
			blocks := []session.ContentBlock{{Type: "text", Text: line}}
			_ = cfg.Session.WriteMessage("user", blocks, nil)
		}

		// Agent phase — Ctrl+C (SIGINT) and ESC cancel the iteration
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		sigDone := make(chan struct{})
		go func() {
			defer close(sigDone)
			select {
			case <-sigCh:
				iterCancel()
			case <-iterCtx.Done():
			}
		}()

		stopESC := watchESC(iterCancel)
		usage, err := agent.Run(iterCtx, provider, disp, &conversation, cfg)
		stopESC()
		totalUsage.Input += usage.Input
		totalUsage.Output += usage.Output
		totalUsage.Reasoning += usage.Reasoning
		totalUsage.CacheRead += usage.CacheRead
		totalUsage.CacheWrite += usage.CacheWrite

		signal.Stop(sigCh)
		iterCancel()   // anuluj kontekst żeby odblokować gorutynę sygnałową
		<-sigDone      // teraz gorutyna wyjdzie przez <-iterCtx.Done()

		if err != nil {
			if errors.Is(err, context.Canceled) {
				// Interrupted by Ctrl+C or ESC — return to prompt
				disp.End()
				fmt.Fprint(os.Stdout, "\n")
				continue
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		disp.End()

		fmt.Fprint(os.Stdout, "\n")
	}
}

// runTUI runs the TUI interactive loop.
func runTUI(provider providers.Provider, modelName string, tuiDisp *display.TUI, cfg agent.Config, baseCtx context.Context, sessionPath string) {
	var conversation []providers.RichMessage
	var totalUsage stream.Usage

	// Replay session history if resuming
	if cfg.Session != nil && cfg.Session.IsResume() && sessionPath != "" {
		replaySessionToDisplay(tuiDisp, sessionPath)
		parsedLines := cfg.Session.Messages()
		rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
		if len(rebuiltMsgs) > 0 {
			conversation = rebuiltMsgs
		}
	}

	// Close TUI on exit, write session end
	defer func() {
		if cfg.Session != nil {
			agent.WriteSessionEnd(cfg.Session, "ok", 0, &totalUsage)
		}
		tuiDisp.Close()
	}()

	for {
		iterCtx, iterCancel := context.WithCancel(baseCtx)

		line, err := tuiDisp.ReadInput(iterCtx, "")
		if err != nil {
			iterCancel()
			return
		}

		line = strings.TrimSpace(line)
		if line == "/exit" {
			iterCancel()
			return
		}
		if line == "/new" {
			iterCancel()
			conversation = nil
			continue
		}
		if line == "" {
			iterCancel()
			continue
		}

		conversation = append(conversation, providers.RichMessage{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: line}},
		})

		if cfg.Session != nil {
			blocks := []session.ContentBlock{{Type: "text", Text: line}}
			_ = cfg.Session.WriteMessage("user", blocks, nil)
		}

		usage, err := agent.Run(iterCtx, provider, tuiDisp, &conversation, cfg)
		iterCancel()
		totalUsage.Input += usage.Input
		totalUsage.Output += usage.Output
		totalUsage.Reasoning += usage.Reasoning
		totalUsage.CacheRead += usage.CacheRead
		totalUsage.CacheWrite += usage.CacheWrite

		tuiDisp.Done(usage, stream.Stats{})

		if err != nil && !errors.Is(err, context.Canceled) {
			return
		}
	}
}

// watchESC starts a goroutine that monitors the terminal for the ESC key (0x1b).
// When ESC is pressed, it calls cancel() to interrupt the current operation.
// It sets stdin to raw+cbreak mode (non-canonical, echo off, ISIG on, OPOST on)
// with VMIN=0 and VTIME=1 (100ms timeout) so the goroutine can exit promptly
// when the context is cancelled externally (e.g. Ctrl+C).
// Returns a cleanup function that restores the original terminal state.
// If stdin is not a terminal, returns a no-op function.
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
	var t syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
		term.Restore(fd, oldState)
		return func() {}
	}
	t.Lflag |= syscall.ISIG
	t.Oflag |= syscall.OPOST
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1 // 100ms
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
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
		close(stop)       // signal goroutine to stop
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
