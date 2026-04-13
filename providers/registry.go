package providers

import (
	"fmt"
	"os"
	"strings"
)

var (
	providers = make(map[string]Provider)
)

func Register(p Provider) {
	providers[p.Name()] = p
}

func ListProviders() []Provider {
	var result []Provider
	for _, p := range providers {
		result = append(result, p)
	}
	return result
}

func GetProvider(name string) (Provider, bool) {
	p, ok := providers[name]
	return p, ok
}

func FindModel(model string) (Provider, string, bool) {
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		if p, ok := providers[parts[0]]; ok {
			return p, parts[1], true
		}
		return nil, "", false
	}
	for _, p := range providers {
		if !p.IsConfigured() {
			continue
		}
		for _, m := range p.Models() {
			if m == model {
				return p, model, true
			}
		}
	}
	return nil, "", false
}

type DefaultHandler struct {
	output *strings.Builder
	done   chan struct{}
}

func NewDefaultHandler() *DefaultHandler {
	return &DefaultHandler{
		output: new(strings.Builder),
		done:   make(chan struct{}),
	}
}

func (h *DefaultHandler) Chunk(text string) {
	fmt.Print(text)
	h.output.WriteString(text)
}

func (h *DefaultHandler) Summary(usage UsageInfo) {
	fmt.Fprintf(os.Stderr, "\n[Tokens: %d in / %d out, Cost: $%.6f]\n",
		usage.InputTokens, usage.OutputTokens, usage.Cost)
}

func (h *DefaultHandler) End() {
	close(h.done)
}

func (h *DefaultHandler) Error(err error) {
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (h *DefaultHandler) Done() <-chan struct{} {
	return h.done
}
