package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/opencode-go"
	_ "github.com/decodo/tyci-agent/providers/opencode-zen"
	"github.com/decodo/tyci-agent/tools"
)

type OutputHandler struct {
	out *os.File
}

func (h *OutputHandler) Chunk(text string) {
	fmt.Fprint(h.out, text)
	h.out.Sync()
}

func (h *OutputHandler) Summary(usage providers.UsageInfo) {}

func (h *OutputHandler) End() {}

func (h *OutputHandler) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (h *OutputHandler) Thinking(text string) {}

func (h *OutputHandler) EndThinking() {}

func (h *OutputHandler) LogToolCallStart(string) {}

func (h *OutputHandler) ToolCallArg(string) {}

func (h *OutputHandler) EndToolCall() {}

func main() {
	debugFlag := flag.Bool("debug", false, "Show HTTP request/response data")
	modelFlag := flag.String("model", "opencode-zen/big-pickle", "Model to use (format: provider/model)")
	promptTextFlag := flag.String("prompt-to-text", "", "Prompt for text response")
	promptJSONFlag := flag.String("prompt-to-json", "", "Prompt for JSON response")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: tyci-agent [--debug] [--model provider/model] (--prompt-to-text <prompt> | --prompt-to-json <prompt>)\n\n")
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

	handler := &OutputHandler{out: os.Stdout}

	messages := []providers.Message{
		{Role: "user", Content: prompt},
	}

	result, err := provider.SendWithHandler(modelName, messages, handler, *debugFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	for len(result.ToolCalls) > 0 {
		toolResults := []string{}
		for _, tc := range result.ToolCalls {
			// Print tool call before executing
			fmt.Fprintf(os.Stderr, "🔧 %s(%s):\n", tc.Name, tc.Arguments)

			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing args for %s: %v\n", tc.Name, err)
				toolResults = append(toolResults, fmt.Sprintf("Error: %v", err))
				continue
			}

			toolRes := tools.RunTool(tc.Name, args)
			if toolRes.Success {
				fmt.Fprintf(os.Stderr, "%s\n", toolRes.Content)
				toolResults = append(toolResults, toolRes.Content)
			} else {
				fmt.Fprintf(os.Stderr, "Error: %s\n", toolRes.Error)
				toolResults = append(toolResults, "Error: "+toolRes.Error)
			}
		}

		messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
		messages = append(messages, providers.Message{Role: "user", Content: "Tool results:\n" + strings.Join(toolResults, "\n---\n")})

		result, err = provider.SendWithHandler(modelName, messages, handler, *debugFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	}

	// For JSON mode, validate and format the output
	if expectJSON && result.Text != "" {
		var jsonData interface{}
		if err := json.Unmarshal([]byte(result.Text), &jsonData); err != nil {
			// Not valid JSON, wrap it
			output := map[string]interface{}{
				"response":   result.Text,
				"tool_calls": result.ToolCalls,
			}
			jsonBytes, _ := json.MarshalIndent(output, "", "  ")
			fmt.Fprintln(os.Stdout, string(jsonBytes))
		} else {
			// Valid JSON, output as-is with indentation
			jsonBytes, _ := json.MarshalIndent(jsonData, "", "  ")
			fmt.Fprintln(os.Stdout, string(jsonBytes))
		}
	} else {
		fmt.Fprintln(os.Stdout)
	}
}
