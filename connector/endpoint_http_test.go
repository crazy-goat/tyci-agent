package connector

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

// stubDoer answers every request from memory and records what it received.
type stubDoer struct {
	got   *http.Request
	calls int
}

func (d *stubDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.got = req
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: [DONE]\n")),
		Request:    req,
	}, nil
}

// Endpoint.HTTP and Endpoint.Headers must reach the wire. HTTP is populated in
// production by providers.NewProvider (from its Deps.HTTP); Headers is still
// always empty, so only a test proves that half of the plumbing exists.
func TestEndpointHTTPAndHeadersReachTheRequest(t *testing.T) {
	doer := &stubDoer{}
	c, err := NewOpenAI(Endpoint{
		BaseURL: "https://api.example.invalid",
		Path:    "/v1/chat/completions",
		APIKey:  "sk-test",
		Headers: map[string]string{"X-Tyci-Test": "1"},
		HTTP:    doer,
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}

	err = c.Stream(context.Background(), Request{Model: "gpt-4"}, func(stream.Event) error { return nil })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if doer.calls != 1 {
		t.Fatalf("injected doer got %d requests, want 1 (Endpoint.HTTP was ignored)", doer.calls)
	}
	if got, want := doer.got.URL.String(), "https://api.example.invalid/v1/chat/completions"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
	if got := doer.got.Header.Get("X-Tyci-Test"); got != "1" {
		t.Errorf("X-Tyci-Test = %q, want %q (Endpoint.Headers was ignored)", got, "1")
	}
}

// A nil Endpoint.HTTP must not panic: it falls through to the api layer's
// shared default client. That is the production path for every provider built
// without its own Deps.HTTP.
func TestEndpointNilHTTPUsesDefaultClient(t *testing.T) {
	c, err := NewOpenAI(Endpoint{BaseURL: "https://127.0.0.1:1", Path: "/v1/chat/completions"})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// The canceled context makes the default client fail fast; all we assert
	// is that we got there at all instead of panicking on a nil doer.
	if err := c.Stream(ctx, Request{Model: "gpt-4"}, func(stream.Event) error { return nil }); err == nil {
		t.Fatal("expected an error from the default client, got nil")
	}
}
