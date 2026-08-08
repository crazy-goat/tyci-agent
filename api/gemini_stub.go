//go:build nogemini

package api

import (
	"context"
	"errors"

	"github.com/decodo/tyci/stream"
)

// GeminiStreamer is a stub when gemini support is excluded at build time.
type GeminiStreamer struct {
	HTTP    HTTPDoer
	Headers map[string]string
}

// Stream returns an error when gemini support is excluded.
func (s GeminiStreamer) Stream(ctx context.Context, _ string, _ string, _ GeminiRequest, _ func(stream.Event) error) error {
	return errors.New("gemini support excluded at build time (rebuild without -tags nogemini)")
}
