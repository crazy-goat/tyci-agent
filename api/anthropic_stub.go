//go:build noanthropic

package api

import (
	"context"
	"errors"

	"github.com/decodo/tyci-agent/stream"
)

func StreamAnthropic(ctx context.Context, _ string, _ string, _ AnthropicRequest, _ func(stream.Event) error) error {
	return errors.New("anthropic support excluded at build time (rebuild without -tags noanthropic)")
}
