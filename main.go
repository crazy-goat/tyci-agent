package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/opencode-go"
	_ "github.com/decodo/tyci-agent/providers/opencode-zen"
	"github.com/decodo/tyci-agent/tools"
)

var (
	bgTools   string
	bgReset   = "\033[0m"
	clearLine = "\033[K"
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
}

func (h *OutputHandler) Chunk(text string) {
	if h.silent {
		if h.buffer != nil {
			h.buffer.WriteString(text)
		}
	} else {
		fmt.Fprint(h.out, text)
		h.out.Sync()
	}
}

func (h *OutputHandler) Summary(usage providers.UsageInfo) {}

func (h *OutputHandler) End() {}

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
		fmt.Fprintf(os.Stderr, "Usage: tyci-agent [--debug] [--model provider/model] [--hide-thinking] [--hide-tools] [--max-retries N] (--prompt-to-text <prompt> | --prompt-to-json <prompt>)\n\n")
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

	// Validate that exactly one prompt flag is provided
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

	model := *modelFlag
	var prompt string
	var expectJSON bool

	if *promptTextFlag != "" {
		prompt = *promptTextFlag
		expectJSON = false
	} else {
		prompt = *promptJSONFlag
		expectJSON = true
	}

	provider, modelName, ok := providers.FindModel(model)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: model %q not found\n", model)
		os.Exit(1)
	}

	providers.DefaultRetryConfig = api.RetryConfig{MaxRetries: *maxRetriesFlag, BaseBackoff: 4, MaxBackoff: 128}

	var textBuffer strings.Builder
	handler := &OutputHandler{
		out:          os.Stdout,
		silent:       expectJSON,
		buffer:       &textBuffer,
		hideThinking: *hideThinkingFlag,
		hideTools:    *hideToolsFlag,
	}

	messages := []providers.Message{
		{Role: "user", Content: prompt},
	}

	result, err := provider.SendWithHandler(modelName, messages, handler, *debugFlag, *hideThinkingFlag, *hideToolsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	toolCount := 0
	for len(result.ToolCalls) > 0 {
		toolResults := []string{}
		for _, tc := range result.ToolCalls {
			if !*hideToolsFlag {
				if toolCount > 0 {
					fmt.Fprintf(os.Stderr, "\n")
				}
				toolCount++
				if tc.Name == "read" {
					fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):%s%s\n", bgTools, clearLine, tc.Name, tc.Arguments, clearLine, bgReset)
				} else {
					fmt.Fprintf(os.Stderr, "%s%s🔧 %s(%s):\n", bgTools, clearLine, tc.Name, tc.Arguments)
				}
			}

			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				if !*hideToolsFlag {
					api.StderrOutput = true
					fmt.Fprintf(os.Stderr, "%sError: %v%s\n", clearLine, err, bgReset)
				}
				toolResults = append(toolResults, fmt.Sprintf("Error: %v", err))
				continue
			}

			toolRes := tools.RunTool(tc.Name, args)
			if toolRes.Success {
				if !*hideToolsFlag {
					api.StderrOutput = true
					if tc.Name != "read" {
						content := strings.ReplaceAll(toolRes.Content, "\n", "\n"+clearLine)
						fmt.Fprintf(os.Stderr, "%s%s%s\n", content, clearLine, bgReset)
					}
				}
				toolResults = append(toolResults, toolRes.Content)
			} else {
				if !*hideToolsFlag {
					api.StderrOutput = true
					fmt.Fprintf(os.Stderr, "%sError: %s%s\n", clearLine, toolRes.Error, bgReset)
				}
				toolResults = append(toolResults, "Error: "+toolRes.Error)
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

	// For JSON mode, validate and format the output
	if expectJSON {
		responseText := textBuffer.String()
		if responseText == "" {
			responseText = result.Text
		}

		if responseText != "" {
			var jsonData interface{}
			if err := json.Unmarshal([]byte(responseText), &jsonData); err != nil {
				// Not valid JSON, wrap it
				output := map[string]interface{}{
					"response": responseText,
				}
				if len(result.ToolCalls) > 0 {
					output["tool_calls"] = result.ToolCalls
				}
				jsonBytes, _ := json.MarshalIndent(output, "", "  ")
				fmt.Fprintln(os.Stdout, string(jsonBytes))
			} else {
				// Valid JSON, output as-is with indentation
				jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
				fmt.Fprintln(os.Stdout, string(jsonBytes))
			}
		}
	} else {
		fmt.Fprintln(os.Stdout)
	}
}
