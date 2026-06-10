//go:build noanthropic

package api

import (
	"context"
	"errors"
)

// AnthropicClient is a stub when anthropic support is excluded at build time.
type AnthropicClient struct{}

// NewAnthropicClient returns an error when anthropic support is excluded.
func NewAnthropicClient(apiKey, endpoint string) *AnthropicClient {
	return &AnthropicClient{}
}

// Stream returns an error when anthropic support is excluded.
func (c *AnthropicClient) Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error {
	return errors.New("anthropic support excluded at build time (rebuild without -tags noanthropic)")
}
