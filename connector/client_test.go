package connector

import (
	"context"
	"testing"

	"github.com/decodo/tyci/stream"
)

// fakeModelClient is a minimal ModelClient for exercising the context
// plumbing and the FullModel helper without a real provider/connector.
type fakeModelClient struct {
	provider string
	model    string
}

func (f fakeModelClient) Provider() string { return f.provider }
func (f fakeModelClient) Model() string    { return f.model }
func (f fakeModelClient) Stream(context.Context, Request) (<-chan stream.Event, error) {
	return nil, nil
}

func TestModelClientFromContext_RoundTrip(t *testing.T) {
	mc := fakeModelClient{provider: "acme", model: "big-1"}
	ctx := WithModelClient(context.Background(), mc)

	got := ModelClientFromContext(ctx)
	if got == nil {
		t.Fatal("ModelClientFromContext returned nil after WithModelClient")
	}
	if got.Provider() != "acme" || got.Model() != "big-1" {
		t.Errorf("got Provider()=%q Model()=%q, want acme/big-1", got.Provider(), got.Model())
	}
}

func TestModelClientFromContext_Empty(t *testing.T) {
	if got := ModelClientFromContext(context.Background()); got != nil {
		t.Errorf("expected nil ModelClient from an empty context, got %v", got)
	}
}

func TestFullModel(t *testing.T) {
	mc := fakeModelClient{provider: "acme", model: "big-1"}
	if got := FullModel(mc); got != "acme/big-1" {
		t.Errorf("FullModel = %q, want %q", got, "acme/big-1")
	}
}
