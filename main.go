package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/opencode-go"
	_ "github.com/decodo/tyci-agent/providers/opencode-zen"
	"github.com/decodo/tyci-agent/tools"
)

func listModels() {
	fmt.Println("Available models:")
	for _, p := range providers.ListProviders() {
		if !p.IsConfigured() {
			continue
		}
		for _, m := range p.Models() {
			fmt.Printf("  ✓ %s/%s\n", p.Name(), m)
		}
	}
}

func main() {
	listFlag := flag.Bool("list", false, "list available models")
	prompt := flag.String("p", "", "prompt (required)")
	system := flag.String("s", "", "system prompt")
	model := flag.String("m", "", "model in format provider/model (e.g., opencode-zen/big-pickle)")
	output := flag.String("o", "stdout", "output file (default: stdout)")
	debugFlag := flag.Bool("debug", false, "debug mode - print requests and responses to stderr")
	flag.Parse()

	if *listFlag {
		listModels()
		os.Exit(0)
	}

	if *model == "" {
		*model = "opencode-zen/big-pickle"
		fmt.Fprintf(os.Stderr, "Using default model: %s (free)\n", *model)
	}

	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "Error: --prompt is required")
		os.Exit(1)
	}

	provider, modelName, ok := providers.FindModel(*model)
	if !ok {
		fmt.Fprintf(os.Stderr, "Error: model %q not found\n", *model)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	var out *os.File = os.Stdout
	if *output != "stdout" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		out = f
	}

	var systemMsg string
	if *system != "" {
		systemMsg = *system
	}

	result, err := provider.Send(ctx, modelName, *prompt, systemMsg, *debugFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprint(out, result.Text)
	out.Sync()

	messages := []providers.Message{
		{Role: "user", Content: *prompt},
	}

	for len(result.ToolCalls) > 0 {
		fmt.Fprintf(os.Stderr, "\n[Executing %d tool call(s)]\n", len(result.ToolCalls))

		toolResults := []string{}
		for i, tc := range result.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				fmt.Fprintf(os.Stderr, "Error: failed to parse tool arguments for %s: %v\n", tc.Name, err)
				toolResults = append(toolResults, fmt.Sprintf("Error parsing arguments for %s: %v", tc.Name, err))
				continue
			}

			result := tools.RunTool(tc.Name, args)
			var resultContent string
			if result.Success {
				resultContent = result.Content
			} else {
				resultContent = "Error: " + result.Error
			}
			toolResults = append(toolResults, resultContent)

			if *debugFlag {
				fmt.Fprintf(os.Stderr, "[TOOL_RESULT %d] %s: %s\n", i, tc.Name, resultContent)
			}
		}

		messages = append(messages, providers.Message{Role: "assistant", Content: result.Text})
		messages = append(messages, providers.Message{Role: "user", Content: "Tool results:\n" + strings.Join(toolResults, "\n---\n")})

		result, err = provider.SendWithMessages(ctx, modelName, *prompt, systemMsg, messages, *debugFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Fprint(out, result.Text)
		out.Sync()
	}
}
