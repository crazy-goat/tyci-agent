package main

import (
	"bufio"
	"context"
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
	"github.com/decodo/tyci-agent/tools"
	"golang.org/x/term"
)

func main() {
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

	providers.RegisterProvidersFromConfig(filepath.Join(os.Getenv("HOME"), ".cache", "tyci-agent", "model.json"))

	var interactiveFlag bool
	noDebugFlag := flag.Bool("no-debug", false, "Disable API request/response debug logging")
	debugFlag := flag.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := flag.String("model", "", "Model to use (format: provider/model)")
	promptFlag := flag.String("prompt", "", "Prompt for response")
	maxRetriesFlag := flag.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	historyFileFlag := flag.String("history-file", "", "Path to history file (default: ~/.local/share/tyci-agent/history)")
	modeFlag := flag.String("mode", "minimal", "Display mode: minimal, normal, interactive")

	flag.Usage = func() {
		fmt.Fprintf(os.Stdout, "Usage: tyci-agent [--debug] [--no-debug] [--model provider/model] [--max-retries N] [--history-file <path>] [--mode minimal|normal|interactive] (--prompt <prompt> | --interactive)\n\n")
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

	model := *modelFlag

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
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q (expected minimal, normal, or interactive)\n", mode)
		os.Exit(1)
	}

	cfg := agent.Config{
		Model:      modelName,
		System:     providers.BuildSystemPrompt(),
		MaxRetries: *maxRetriesFlag,
		Debug:      *debugFlag,
		Tools:      toolsAdapter{},
		Schema:     tools.GetToolsSchemaJSON(),
	}
	tools.SetProvider(provider)
	tools.SetCurrentModel(modelName)

	if interactiveFlag {
		runInteractive(provider, modelName, disp, historyFile, cfg, ctx)
		return
	}

	if *promptFlag == "" {
		fmt.Fprintln(os.Stderr, "Error: must provide --prompt")
		flag.Usage()
		os.Exit(1)
	}

	messages := []providers.Message{{Role: "user", Content: *promptFlag}}
	if err := agent.Run(ctx, provider, disp, &messages, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	disp.End()
	fmt.Fprintln(os.Stdout)
}

func runInteractive(provider providers.Provider, modelName string, disp display.Display, historyFile string, cfg agent.Config, baseCtx context.Context) {
	var conversation []providers.Message

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

	sigCtx, sigCancel := signal.NotifyContext(baseCtx, os.Interrupt)
	defer sigCancel()

	for {
		var line string
		var err error

		if editor != nil {
			line, err = editor.Read(sigCtx, ">>> ")
		} else {
			line, err = simplePrompt(">>> ")
		}

		if errors.Is(err, readline.ErrEOF) {
			fmt.Println("Bye!")
			return
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, readline.ErrInterrupt) {
				// Ctrl+C or ESC during input — return to prompt
				fmt.Fprint(os.Stdout, "\n")
				continue
			}
			fmt.Fprintf(os.Stdout, "Read error: %v\n", err)
			continue
		}

		line = strings.TrimSpace(line)
		if line == "/exit" {
			fmt.Println("Bye!")
			return
		}
		if line == "/new" {
			conversation = nil
			fmt.Print("\033[2J\033[H")
			continue
		}
		if line == "" {
			continue
		}

		if editor != nil {
			editor.AddHistory(line)
		}

		conversation = append(conversation, providers.Message{Role: "user", Content: line})

		// Per-iteration context — cancelling it returns to prompt instead of exiting
		iterCtx, iterCancel := context.WithCancel(sigCtx)

		// Watch for ESC key during agent execution to cancel iteration
		stopESC := watchESC(iterCancel)
		err = agent.Run(iterCtx, provider, disp, &conversation, cfg)
		stopESC()
		iterCancel()

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

// watchESC starts a goroutine that monitors the terminal for the ESC key (0x1b).
// When ESC is pressed, it calls cancel() to interrupt the current operation.
// It sets stdin to cbreak mode (non-canonical, echo off, ISIG on, OPOST on)
// so that individual keypresses can be read without breaking display output.
// Returns a cleanup function that restores the original terminal state.
// If stdin is not a terminal, returns a no-op function.
func watchESC(cancel context.CancelFunc) func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}

	// Save current terminal state
	oldState, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}

	// Set raw mode first (non-canonical, no echo, etc.)
	_, err = term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}

	// Re-enable ISIG (signal generation for Ctrl+C) and OPOST (output processing)
	// while keeping non-canonical mode and echo off.
	var t syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
		term.Restore(fd, oldState)
		return func() {}
	}
	t.Lflag |= syscall.ISIG
	t.Oflag |= syscall.OPOST
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
		term.Restore(fd, oldState)
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				// read interrupted (e.g. by Ctrl+C signal) or EOF
				return
			}
			if buf[0] == 0x1b { // ESC
				cancel()
				return
			}
		}
	}()

	return func() {
		term.Restore(fd, oldState)
		<-done
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
