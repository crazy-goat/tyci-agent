package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/decodo/tyci-agent/providers"
	_ "github.com/decodo/tyci-agent/providers/anthropic"
	_ "github.com/decodo/tyci-agent/providers/openai"
	_ "github.com/decodo/tyci-agent/providers/zen"
)

type writerHandler struct {
	out *os.File
}

func (h *writerHandler) Chunk(text string) {
	fmt.Fprint(h.out, text)
	h.out.Sync()
}

func (h *writerHandler) Summary(usage providers.UsageInfo) {
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		fmt.Fprintf(os.Stderr, "\n[Tokens: %d in / %d out, Cost: $%.6f]\n",
			usage.InputTokens, usage.OutputTokens, usage.Cost)
	}
}

func (h *writerHandler) End() {}

func (h *writerHandler) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

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
	model := flag.String("m", "", "model in format provider/model (e.g., zen/glm-5.1)")
	output := flag.String("o", "stdout", "output file (default: stdout)")
	flag.Parse()

	if *listFlag || *model == "" {
		listModels()
		os.Exit(0)
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

	handler := &writerHandler{out: out}
	err := provider.Send(ctx, modelName, *prompt, *system, handler)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
