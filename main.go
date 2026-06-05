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
	"strings"
	"sync"
	"time"

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
	hideThinkingFlag := flag.Bool("hide-thinking", false, "Hide thinking output (💭)")
	hideToolsFlag := flag.Bool("hide-tools", false, "Hide tool call output (🔧)")
	maxRetriesFlag := flag.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")
	historyFileFlag := flag.String("history-file", "", "Path to history file (default: ~/.local/share/tyci-agent/history)")
	noHistoryFlag := flag.Bool("no-history", false, "Disable history loading/saving entirely")
	modeFlag := flag.String("mode", "minimal", "Display mode: minimal, normal, interactive")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tyci-agent [--debug] [--model provider/model] [--hide-thinking] [--hide-tools] [--max-retries N] [--history-file <path>] [--no-history] [--mode minimal|normal|interactive] (--prompt-to-text <prompt> | --prompt-to-json <prompt> | --interactive)\n\n")
		fmt.Fprintf(os.Stderr, "Available models:\n")
		for _, p := range providers.ListProviders() {
			for _, m := range p.Models() {
				fmt.Fprintf(os.Stderr, "  %s/%s\n", p.Name(), m)
			}
		}
		fmt.Fprintf(os.Stderr, "\nFree models:\n")
		for _, p := range providers.ListProviders() {
			for _, m := range p.FreeModels() {
				fmt.Fprintf(os.Stderr, "  %s/%s (free)\n", p.Name(), m)
			}
		}
		flag.PrintDefaults()
	}
	flag.Parse()

	var historyFile string
	if *noHistoryFlag {
		historyFile = ""
	} else if *historyFileFlag != "" {
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
	var prompt string
	var expectJSON bool

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: model %q not found\n", model)
		os.Exit(1)
	}

	var disp display.Display
	mode := *modeFlag
	switch mode {
	case "minimal":
		disp = display.NewMinimal(*hideThinkingFlag, *hideToolsFlag)
	case "normal":
		if *promptJSONFlag != "" {
			disp = display.NewJSON()
		} else {
			disp = display.NewTerminal(*hideThinkingFlag, *hideToolsFlag, false)
		}
	case "interactive":
		*interactiveFlag = true
		disp = display.NewTerminal(*hideThinkingFlag, *hideToolsFlag, true)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown mode %q (expected minimal, normal, or interactive)\n", mode)
		os.Exit(1)
	}

	if *interactiveFlag {
		var conversation []providers.Message

		var editor *readline.LineEditor
		if *noHistoryFlag {
			editor = nil
		} else {
			var err error
			editor, err = readline.New(historyFile, readline.DefaultMaxEntries)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: cannot init readline: %v\n", err)
				editor = nil
			}
		}
		if editor != nil {
			defer editor.Close()
		}

		if *promptTextFlag != "" || *promptJSONFlag != "" {
			if *promptTextFlag != "" {
				prompt = *promptTextFlag
			} else {
				prompt = *promptJSONFlag
			}

			conversation = append(conversation, providers.Message{Role: "user", Content: prompt})
			conversation = runLLMLoop(context.Background(), provider, modelName, conversation, disp, *debugFlag)
			disp.End()
			fmt.Fprint(os.Stdout, "\n")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()

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
					ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
					continue
				}
				fmt.Fprintf(os.Stderr, "Read error: %v\n", err)
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
			conversation = runLLMLoop(ctx, provider, modelName, conversation, disp, *debugFlag)
			disp.End()

			fmt.Fprint(os.Stdout, "\n")
		}
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

	if *promptTextFlag != "" {
		prompt = *promptTextFlag
		expectJSON = false
	} else {
		prompt = *promptJSONFlag
		expectJSON = true
	}

	messages := []providers.Message{{Role: "user", Content: prompt}}
	runLLMLoop(context.Background(), provider, modelName, messages, disp, *debugFlag)
	disp.End()
	if !expectJSON {
		fmt.Fprintln(os.Stdout)
	}
}

type toolRunResult struct {
	success bool
	content string
	err     string
}

func runLLMLoop(ctx context.Context, provider providers.Provider, modelName string, messages []providers.Message, disp display.Display, debug bool) []providers.Message {
	var totalInputTokens, totalOutputTokens, totalReasoningTokens int

	result, err := provider.SendWithHandler(ctx, modelName, messages, disp, debug)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return messages
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	totalInputTokens += result.InputTokens
	totalOutputTokens += result.OutputTokens
	totalReasoningTokens += result.ReasoningTokens

loop:
	for {
		if len(result.ToolCalls) > 0 {
			toolResults := make([]string, len(result.ToolCalls))
			parsedArgs := make([]map[string]any, len(result.ToolCalls))

			for i, tc := range result.ToolCalls {
				if err := json.Unmarshal([]byte(tc.Arguments), &parsedArgs[i]); err != nil {
					toolResults[i] = fmt.Sprintf("Error: %v", err)
					parsedArgs[i] = nil
				}
			}

			var wg sync.WaitGroup
			results := make([]toolRunResult, len(result.ToolCalls))

			for i, tc := range result.ToolCalls {
				if parsedArgs[i] == nil {
					continue
				}

				wg.Add(1)
				go func(idx int, tc providers.ToolCall, args map[string]any) {
					defer wg.Done()
					toolCtx, toolCancel := context.WithTimeout(ctx, 5*time.Minute)
					defer toolCancel()
					toolRes := tools.RunTool(toolCtx, tc.Name, args)
					results[idx] = toolRunResult{
						success: toolRes.Success,
						content: toolRes.Content,
						err:     toolRes.Error,
					}
				}(i, tc, parsedArgs[i])
			}

			wg.Wait()

			if errors.Is(ctx.Err(), context.Canceled) {
				return messages
			}

			for i, tc := range result.ToolCalls {
				if parsedArgs[i] == nil {
					continue
				}
				r := results[i]
				if r.success {
					toolResults[i] = r.content
				} else {
					toolResults[i] = "Error: " + r.err
				}
				disp.ToolResult(tc.Name, &display.ToolResult{
					Success: r.success,
					Content: r.content,
					Error:   r.err,
				})
			}

			messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
			messages = append(messages, providers.Message{Role: "user", Content: "Tool results:\n" + strings.Join(toolResults, "\n---\n")})

			result, err = provider.SendWithHandler(ctx, modelName, messages, disp, debug)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return messages
				}
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			totalInputTokens += result.InputTokens
			totalOutputTokens += result.OutputTokens
			totalReasoningTokens += result.ReasoningTokens
			continue
		}

		switch result.StopReason {
		case "stop":
			break loop
		case "tool_calls":
			if len(result.ToolCalls) > 0 {
				continue
			}
		case "length", "content_filter", "function_call":
			if result.Text == "" {
				continue
			}
		default:
			if result.Text == "" {
				continue
			}
		}

		messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
		result, err = provider.SendWithHandler(ctx, modelName, messages, disp, debug)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return messages
			}
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		totalInputTokens += result.InputTokens
		totalOutputTokens += result.OutputTokens
		totalReasoningTokens += result.ReasoningTokens
	}

	messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
	return messages
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
