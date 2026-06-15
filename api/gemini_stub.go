//go:build nogemini

package api

import (
	"context"
	"errors"

	"github.com/decodo/tyci/stream"
)

func StreamGemini(ctx context.Context, _ string, _ string, _ GeminiRequest, _ func(stream.Event) error) error {
	return errors.New("gemini support excluded at build time (rebuild without -tags nogemini)")
}
