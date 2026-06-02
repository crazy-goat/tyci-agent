package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/opencode-go"
	_ "github.com/decodo/tyci-agent/providers/opencode-zen"
	"github.com/decodo/tyci-agent/tools"
)

var (
	bgTools        string
	bgReset        = "\033[0m"
	clearLine      = "\033[K"
	interactiveFlag = flag.Bool("interactive", false, "Interactive mode: read prompts from stdin")
)

func init() {
	if api.TerminalIsDark() {
		bgTools = "\033[48;2;18;18;42m"
	} else {
		bgTools = "\033[48;2;248;248;254m"
	}
}

type OutputHandler struct {
	out          *os.File
	buffer       *strings.Builder
	silent       bool
	hideThinking bool
	hideTools    bool
	started      bool
	LastText     string
	LastToolCalls []providers.ToolCall
}

func (h *OutputHandler) Chunk(text string) {
	if h.silent {
		if h.buffer != nil {
			h.buffer.WriteString(text)
		}
		return
	}

	if !h.started {
		fmt.Fprint(h.out, bgTools)
		h.started = true
	}
	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)
	fmt.Fprint(h.out, text)
	h.out.Sync()
}

func (h *OutputHandler) End() {
	if !h.silent && h.started {
		fmt.Fprint(h.out, clearLine+bgReset)
		h.out.Sync()
		h.started = false
	}
}

func (h *OutputHandler) Summary(usage providers.UsageInfo) {}

func (h *OutputHandler) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (h *OutputHandler) Thinking(text string) {}

func (h *OutputHandler) EndThinking() {}

func (h *OutputHandler) LogToolCallStart(name string) {}

func (h *OutputHandler) ToolCallArg(text string) {}

func (h *OutputHandler) EndToolCall() {}

func main() {
	debugFlag := flag.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := flag.String("model", "opencode-zen/big-pickle", "Model to use (format: provider/model)")
	promptTextFlag := flag.String("prompt-to-text", "", "Prompt for text response")
	promptJSONFlag := flag.String("prompt-to-json", "", "Prompt for JSON response")
	hideThinkingFlag := flag.Bool("hide-thinking", false, "Hide thinking output (💭)")
	hideToolsFlag := flag.Bool("hide-tools", false, "Hide tool call output (🔧)")
	maxRetriesFlag := flag.Int("max-retries", 5, "Max retries on transient errors (0 to disable)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tyci-agent [--debug] [--model provider/model] [--hide-thinking] [--hide-tools] [--max-retries N] (--prompt-to-text <prompt> | --prompt-to-json <prompt> | --interactive)\n\n")
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

	providers.DefaultRetryConfig = api.RetryConfig{MaxRetries: *maxRetriesFlag, BaseBackoff: 4, MaxBackoff: 128}

	model := *modelFlag
	var prompt string
	var expectJSON bool

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: model %q not found\n", model)
		os.Exit(1)
	}

	// Interactive mode
	if *interactiveFlag {
		hadInitial := false
		if *promptTextFlag != "" || *promptJSONFlag != "" {
			if *promptTextFlag != "" {
				prompt = *promptTextFlag
			} else {
				prompt = *promptJSONFlag
			}

			handler := &OutputHandler{
				out:          os.Stdout,
				silent:       false,
				buffer:       nil,
				hideThinking: *hideThinkingFlag,
				hideTools:    *hideToolsFlag,
			}

			messages := []providers.Message{{Role: "user", Content: prompt}}
			runLLMLoop(provider, modelName, messages, handler, expectJSON, debugFlag, hideThinkingFlag, hideToolsFlag)
			hadInitial = true
		}

		scanner := bufio.NewScanner(os.Stdin)
		if hadInitial {
			fmt.Fprint(os.Stdout, "\n")
		}
		fmt.Fprint(os.Stdout, ">>> ")
		for scanner.Scan() {
			line := scanner.Text()
			if line == "/exit" {
				break
			}
			if line == "" {
				fmt.Fprint(os.Stdout, ">>> ")
				continue
			}

			handler := &OutputHandler{
				out:          os.Stdout,
				silent:       false,
				buffer:       nil,
				hideThinking: *hideThinkingFlag,
				hideTools:    *hideToolsFlag,
			}

			messages := []providers.Message{{Role: "user", Content: line}}
			runLLMLoop(provider, modelName, messages, handler, expectJSON, debugFlag, hideThinkingFlag, hideToolsFlag)

			fmt.Fprint(os.Stdout, "\n>>> ")
		}
		return
	}

	// Non-interactive mode: validate that exactly one prompt flag is provided
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

	var textBuffer strings.Builder
	handler := &OutputHandler{
		out:          os.Stdout,
		silent:       expectJSON,
		buffer:       &textBuffer,
		hideThinking: *hideThinkingFlag,
		hideTools:    *hideToolsFlag,
	}

	messages := []providers.Message{{Role: "user", Content: prompt}}
	runLLMLoop(provider, modelName, messages, handler, expectJSON, debugFlag, hideThinkingFlag, hideToolsFlag)

	// For JSON mode, validate and format the output
	if expectJSON {
		responseText := textBuffer.String()
		if responseText == "" {
			responseText = handler.LastText
		}

		if responseText != "" {
			var jsonData interface{}
			if err := json.Unmarshal([]byte(responseText), &jsonData); err != nil {
				output := map[string]interface{}{
					"response": responseText,
				}
				if handler.LastToolCalls != nil {
					output["tool_calls"] = handler.LastToolCalls
				}
				jsonBytes, _ := json.MarshalIndent(output, "", "  ")
				fmt.Fprintln(os.Stdout, string(jsonBytes))
			} else {
				jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
				fmt.Fprintln(os.Stdout, string(jsonBytes))
			}
		}
	} else {
		fmt.Fprintln(os.Stdout)
	}
}

func runLLMLoop(provider providers.Provider, modelName string, messages []providers.Message, handler *OutputHandler, expectJSON bool, debugFlag, hideThinkingFlag, hideToolsFlag *bool) {
	result, err := provider.SendWithHandler(modelName, messages, handler, *debugFlag, *hideThinkingFlag, *hideToolsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	toolCount := 0
	for len(result.ToolCalls) > 0 {
		toolResults := make([]string, len(result.ToolCalls))
		parsedArgs := make([]map[string]any, len(result.ToolCalls))

		for i, tc := range result.ToolCalls {
			if err := json.Unmarshal([]byte(tc.Arguments), &parsedArgs[i]); err != nil {
				toolResults[i] = fmt.Sprintf("Error: %v", err)
				parsedArgs[i] = nil
			}
		}

		var wg sync.WaitGroup
		resultBufs := make([]*strings.Builder, len(result.ToolCalls))

		for i, tc := range result.ToolCalls {
			if parsedArgs[i] == nil {
				continue
			}

			buf := &strings.Builder{}
			resultBufs[i] = buf
			wg.Add(1)

			go func(idx int, tc providers.ToolCall, args map[string]any, buf *strings.Builder) {
				defer wg.Done()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()

				toolRes := tools.RunTool(ctx, tc.Name, args)
				if toolRes.Success {
					toolResults[idx] = toolRes.Content
					if !*hideToolsFlag && tc.Name != "read" {
						content := strings.ReplaceAll(toolRes.Content, "\n", "\n"+clearLine)
						fmt.Fprintf(buf, "%s%s%s\n", content, clearLine, bgReset)
					}
				} else {
					toolResults[idx] = "Error: " + toolRes.Error
					if !*hideToolsFlag {
						fmt.Fprintf(buf, "%sError: %s%s\n", clearLine, toolRes.Error, bgReset)
					}
				}
			}(i, tc, parsedArgs[i], buf)
		}

		wg.Wait()

		api.StderrOutput = true
		for i, tc := range result.ToolCalls {
			if parsedArgs[i] == nil {
				continue
			}

			if toolCount > 0 {
				fmt.Fprintln(os.Stderr)
			}
			toolCount++

			title := ""
			if d, ok := parsedArgs[i]["description"].(string); ok && d != "" {
				title = d
			}

			if title != "" {
				cmd := ""
				if c, ok := parsedArgs[i]["command"].(string); ok && c != "" {
					cmd = c
				}
				if cmd != "" {
					fmt.Fprintf(os.Stderr, "%s%s🔧 %s\n%s$ %s%s%s\n", bgTools, clearLine, title, clearLine, cmd, clearLine, bgReset)
				} else {
					fmt.Fprintf(os.Stderr, "%s%s🔧 %s%s%s\n", bgTools, clearLine, title, clearLine, bgReset)
				}
			} else if tc.Name == "read" {
				fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):%s%s\n", bgTools, clearLine, tc.Name, tc.Arguments, clearLine, bgReset)
			} else {
				fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):\n", bgTools, clearLine, tc.Name, tc.Arguments)
			}

			if buf := resultBufs[i]; buf != nil && buf.Len() > 0 {
				fmt.Fprint(os.Stderr, clearLine)
				fmt.Fprint(os.Stderr, buf.String())
			}
		}

		messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
		messages = append(messages, providers.Message{Role: "user", Content: "Tool results:\n" + strings.Join(toolResults, "\n---\n")})

		result, err = provider.SendWithHandler(modelName, messages, handler, *debugFlag, *hideThinkingFlag, *hideToolsFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	handler.LastText = result.Text
	handler.LastToolCalls = result.ToolCalls
}
