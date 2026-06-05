package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/decodo/tyci-agent/agent"
	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/internal/readline"
	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/opencode-go"
	_ "github.com/decodo/tyci-agent/providers/opencode-zen"
	"github.com/decodo/tyci-agent/tools"
)

var interactiveFlag = flag.Bool("interactive", false, "Interactive mode: read prompts from stdin")

func main() {
	debugFlag := flag.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := flag.String("model", "opencode-zen/big-pickle", "Model to use (format: provider/model)")
	promptTextFlag := flag.String("prompt-to-text", "", "Prompt for text response")
	promptJSONFlag := flag.String("prompt-to-json", "", "Prompt for JSON response")
	maxRetriesFlag := flag.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	historyFileFlag := flag.String("history-file", "", "Path to history file (default: ~/.local/share/tyci-agent/history)")
	modeFlag := flag.String("mode", "minimal", "Display mode: minimal, normal, interactive")

	flag.Usage = func() {
		fmt.Fprintf(os.Stdout, "Usage: tyci-agent [--debug] [--model provider/model] [--max-retries N] [--history-file <path>] [--mode minimal|normal|interactive] (--prompt-to-text <prompt> | --prompt-to-json <prompt> | --interactive)\n\n")
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

	var disp display.Display
	mode := *modeFlag
	switch mode {
	case "minimal":
		disp = display.NewMinimal()
	case "normal":
		if *promptJSONFlag != "" {
			disp = display.NewJSON()
		} else {
			disp = display.NewTerminal()
		}
	case "interactive":
		*interactiveFlag = true
		disp = display.NewTerminal()
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q (expected minimal, normal, or interactive)\n", mode)
		os.Exit(1)
	}

	cfg := agent.Config{
		Model:      modelName,
		MaxRetries: *maxRetriesFlag,
		Debug:      *debugFlag,
		Tools:      toolsAdapter{},
		Schema:     tools.GetToolsSchemaJSON(),
	}
	tools.SetProvider(provider)
	tools.SetCurrentModel(modelName)

	if *interactiveFlag {
		runInteractive(provider, modelName, disp, historyFile, cfg)
		return
	}

	if *promptTextFlag == "" && *promptJSONFlag == "" {
		fmt.Fprintln(os.Stderr, "Error: must provide either --prompt-to-text or --prompt-to-json")
		flag.Usage()
		os.Exit(1)
	}
	if *promptTextFlag != "" && *promptJSONFlag != "" {
		fmt.Fprintln(os.Stderr, "Error: cannot use both --prompt-to-text and --prompt-to-json")
		flag.Usage()
		os.Exit(1)
	}

	var prompt string
	var expectJSON bool
	if *promptTextFlag != "" {
		prompt = *promptTextFlag
		expectJSON = false
	} else {
		prompt = *promptJSONFlag
		expectJSON = true
	}

	messages := []providers.Message{{Role: "user", Content: prompt}}
	if err := agent.Run(context.Background(), provider, disp, &messages, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	disp.End()
	if !expectJSON {
		fmt.Fprintln(os.Stdout)
	}
}

func runInteractive(provider providers.Provider, modelName string, disp display.Display, historyFile string, cfg agent.Config) {
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	_ = modelName

	for {
		var line string
		var err error

		if editor != nil {
			line, err = editor.Read(ctx, ">>> ")
		} else {
			line, err = simplePrompt(">>> ")
		}

		if errors.Is(err, readline.ErrEOF) {
			fmt.Println("Bye!")
			return
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, readline.ErrInterrupt) {
				fmt.Println("Bye!")
				return
			}
			fmt.Fprintf(os.Stdout, "Read error: %v\n", err)
			continue
		}

		line = strings.TrimSpace(line)
		if line == "/exit" {
			fmt.Println("Bye!")
			return
		}
		if line == "" {
			continue
		}

		if editor != nil {
			editor.AddHistory(line)
		}

		conversation = append(conversation, providers.Message{Role: "user", Content: line})
		if err := agent.Run(ctx, provider, disp, &conversation, cfg); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Println("Bye!")
				return
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return
		}
		disp.End()

		fmt.Fprint(os.Stdout, "\n")
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
