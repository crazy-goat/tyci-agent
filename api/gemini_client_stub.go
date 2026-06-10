//go:build nogemini

package api

import (
	"context"
	"errors"
)

// GeminiClient is a stub when gemini support is excluded at build time.
type GeminiClient struct{}

// NewGeminiClient returns an error when gemini support is excluded.
func NewGeminiClient(apiKey, endpoint string) *GeminiClient {
	return &GeminiClient{}
}

// Stream returns an error when gemini support is excluded.
func (c *GeminiClient) Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error {
	return errors.New("gemini support excluded at build time (rebuild without -tags nogemini)")
}
