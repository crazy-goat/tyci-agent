package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
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

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tyci-agent [--debug] [--model provider/model] [--hide-thinking] [--hide-tools] [--max-retries N] [--history-file <path>] [--no-history] (--prompt-to-text <prompt> | --prompt-to-json <prompt> | --interactive)\n\n")
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
	if *interactiveFlag {
		disp = display.NewTerminal(*hideThinkingFlag, *hideToolsFlag)
	} else if *promptJSONFlag != "" {
		disp = display.NewJSON()
	} else {
		disp = display.NewTerminal(*hideThinkingFlag, *hideToolsFlag)
	}

	if *interactiveFlag {
		var conversation []providers.Message

		editor, err := readline.New(historyFile, readline.DefaultMaxEntries)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer editor.Close()

		if *promptTextFlag != "" || *promptJSONFlag != "" {
			if *promptTextFlag != "" {
				prompt = *promptTextFlag
			} else {
				prompt = *promptJSONFlag
			}

			conversation = append(conversation, providers.Message{Role: "user", Content: prompt})
			conversation = runLLMLoop(provider, modelName, conversation, disp, *debugFlag)
			disp.End()
			fmt.Fprint(os.Stdout, "\n")
		}

		fmt.Fprint(os.Stdout, ">>> ")
		for {
			line, err := editor.ReadLine()
			if err != nil {
				break
			}
			if line == "/exit" {
				break
			}
			if line == "" {
				fmt.Fprint(os.Stdout, ">>> ")
				continue
			}

			editor.AddHistory(line)
			conversation = append(conversation, providers.Message{Role: "user", Content: line})
			conversation = runLLMLoop(provider, modelName, conversation, disp, *debugFlag)
			disp.End()

			fmt.Fprint(os.Stdout, "\n>>> ")
		}
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

	if *promptTextFlag != "" {
		prompt = *promptTextFlag
		expectJSON = false
	} else {
		prompt = *promptJSONFlag
		expectJSON = true
	}

	messages := []providers.Message{{Role: "user", Content: prompt}}
	runLLMLoop(provider, modelName, messages, disp, *debugFlag)
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

func runLLMLoop(provider providers.Provider, modelName string, messages []providers.Message, disp display.Display, debug bool) []providers.Message {
	result, err := provider.SendWithHandler(modelName, messages, disp, debug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

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
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					toolRes := tools.RunTool(ctx, tc.Name, args)
					results[idx] = toolRunResult{
						success: toolRes.Success,
						content: toolRes.Content,
						err:     toolRes.Error,
					}
				}(i, tc, parsedArgs[i])
			}

			wg.Wait()

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

			result, err = provider.SendWithHandler(modelName, messages, disp, debug)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			continue
		}

		if result.StopReason == "stop" && result.Text != "" {
			break
		}

		messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
		result, err = provider.SendWithHandler(modelName, messages, disp, debug)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
	return messages
}
