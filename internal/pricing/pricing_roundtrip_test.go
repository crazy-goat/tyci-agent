package pricing

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/decodo/tyci/internal/connect"
)

// fakeModelsDevDoer is a connect.HTTPDoer that returns a canned models.dev
// response regardless of the request, so the refresh path can be exercised
// without a network call.
type fakeModelsDevDoer struct {
	body string
}

func (f fakeModelsDevDoer) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Header:     make(http.Header),
	}, nil
}

// This is the regression that started it all: models.dev's response carries
// cost and limit, and an earlier build's cache write silently dropped both
// (see doc comment on connect.ModelsDevModel). This test takes a
// models.dev-shaped payload with cost/limit populated, runs it through
// connect.RefreshModels (faking the HTTP layer, no network call), and reads
// the result back through pricing.Lookup — the same path the TUI status bar
// uses — to confirm the prices and limits actually survive the round trip.
func TestRefreshModels_RoundTripPreservesPrices(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	const payload = `{
  "anthropic": {
    "id": "anthropic",
    "npm": "@ai-sdk/anthropic",
    "name": "Anthropic",
    "api": "anthropic",
    "models": {
      "claude-x": {
        "id": "claude-x",
        "name": "Claude X",
        "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
        "limit": {"context": 200000, "output": 8192}
      }
    }
  }
}`

	restore := connect.SetHTTPClientForTests(fakeModelsDevDoer{body: payload})
	t.Cleanup(restore)

	if _, _, _, err := connect.RefreshModels("", false); err != nil {
		t.Fatalf("RefreshModels() error: %v", err)
	}

	Reset()
	t.Cleanup(Reset)

	rates, limits := Lookup("anthropic", "claude-x")
	if !rates.Known() {
		t.Fatalf("rates not known after round trip: %+v", rates)
	}
	if rates.Input != 3 || rates.Output != 15 || rates.CacheRead != 0.3 || rates.CacheWrite != 3.75 {
		t.Errorf("rates = %+v, want {3 15 0.3 3.75}", rates)
	}
	if limits.Context != 200000 || limits.Output != 8192 {
		t.Errorf("limits = %+v, want {200000 8192}", limits)
	}
}
