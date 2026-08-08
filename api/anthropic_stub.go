//go:build noanthropic

package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/decodo/tyci/stream"
)

// AnthropicStreamer is a stub when anthropic support is excluded at build time.
type AnthropicStreamer struct {
	HTTP    HTTPDoer
	Headers map[string]string
}

// Stream returns an error when anthropic support is excluded.
func (s AnthropicStreamer) Stream(ctx context.Context, _ string, _ string, _ AnthropicRequest, _ func(stream.Event) error) error {
	return errors.New("anthropic support excluded at build time (rebuild without -tags noanthropic)")
}

// ConvertToolsToAnthropic is a no-op stub when anthropic support is excluded.
func ConvertToolsToAnthropic(tools json.RawMessage) json.RawMessage {
	return tools
}
