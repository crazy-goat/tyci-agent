package agent

import (
	"context"
	"testing"

	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
)

type mockProvider struct {
	chunks []string
}

func (m *mockProvider) Name() string  { return "mock" }
func (m *mockProvider) IsConfigured() bool { return true }
func (m *mockProvider) Models() []string  { return []string{"mock-1"} }
func (m *mockProvider) FreeModels() []string { return nil }

func (m *mockProvider) Stream(ctx context.Context, req providers.Request) (<-chan stream.Event, error) {
	ch := make(chan stream.Event, len(m.chunks)+2)
	go func() {
		defer close(ch)
		for _, c := range m.chunks {
			select {
			case ch <- stream.TextDelta{Text: c}:
			case <-ctx.Done():
				return
			}
		}
		ch <- stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}}
	}()
	return ch, nil
}

func TestRunAppendsAssistantMessage(t *testing.T) {
	p := &mockProvider{chunks: []string{"Hello", " world"}}
	d := display.NewSilent()
	msgs := []providers.Message{{Role: "user", Content: "Hi"}}

	if err := Run(context.Background(), p, d, &msgs, Config{Model: "mock-1", MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected assistant role, got %q", msgs[1].Role)
	}
	if msgs[1].Content != "Hello world" {
		t.Errorf("expected %q, got %q", "Hello world", msgs[1].Content)
	}
}

func TestRunSkipsEmptyAssistantMessage(t *testing.T) {
	p := &mockProvider{chunks: nil}
	d := display.NewSilent()
	msgs := []providers.Message{{Role: "user", Content: "Hi"}}

	if err := Run(context.Background(), p, d, &msgs, Config{Model: "mock-1", MaxRetries: 1}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d: %#v", len(msgs), msgs)
	}
}
