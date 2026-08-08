package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

type fallbackState struct {
	active    bool // true if we've switched to a fallback
	idx       int  // index into FallbackModels currently in use (-1 = primary)
	provider  providers.Provider
	model     string
	fullModel string
}

func tryFallback(ctx context.Context, d Sink, msgs *[]connector.Message, cfg Config, fs *fallbackState, totalUsage *stream.Usage, origErr error) (bool, error) {
	var lastErr error

	// Format the reason from the original error
	reason := formatFallbackReason(origErr)

	for fs.idx+1 < len(cfg.FallbackModels) {
		fs.idx++
		fbFull := cfg.FallbackModels[fs.idx]

		// Resolve the fallback model to a provider
		fbProvider, fbModel, ok := providers.FindModel(fbFull)
		if !ok {
			d.ToolBlock(fmt.Sprintf("fallback model %q not found, skipping", fbFull))
			lastErr = fmt.Errorf("fallback model %q not found", fbFull)
			continue
		}

		d.ToolBlock(fmt.Sprintf("Switching to fallback model: %s\nReason: %s", fbFull, reason))

		// Try the fallback
		more, _, _, err := runOnce(ctx, fbProvider, d, msgs, cfg.withModel(fbModel), totalUsage)
		if err != nil {
			lastErr = err
			d.ToolBlock(fmt.Sprintf("fallback %s also failed: %v", fbFull, err))
			continue
		}

		// Fallback succeeded — update state
		fs.active = true
		fs.provider = fbProvider
		fs.model = fbModel
		fs.fullModel = fbFull

		return more, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no fallback models available")
	}
	return false, lastErr
}

// formatFallbackReason extracts a human-readable reason from an error.
// It checks for HTTP status codes, JSON parse errors, and retryable errors.
func formatFallbackReason(err error) string {
	if err == nil {
		return "unknown error"
	}

	// Check for RetryableError (has status code)
	var re *api.RetryableError
	if errors.As(err, &re) {
		return fmt.Sprintf("HTTP %d: %s", re.Code, re.Message)
	}

	// Check for JSON parse errors
	errStr := err.Error()
	if strings.Contains(errStr, "invalid character") ||
		strings.Contains(errStr, "unexpected end of JSON") ||
		strings.Contains(errStr, "JSON parse") ||
		strings.Contains(errStr, "json: cannot unmarshal") ||
		strings.Contains(errStr, "syntax error") {
		return "no JSON in response: " + errStr
	}

	// Check for HTTP errors (from fmt.Errorf("server returned %d: ..."))
	if strings.Contains(errStr, "server returned") {
		return errStr
	}

	// Check for context errors
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	if errors.Is(err, context.Canceled) {
		return "request cancelled"
	}

	// Network errors
	var netErr net.Error
	if errors.As(err, &netErr) {
		return fmt.Sprintf("network error: %s", netErr.Error())
	}

	// Fallback
	return errStr
}

func sleepWithCountdown(ctx context.Context, backoff time.Duration) error {
	remaining := int(backoff.Seconds())
	for remaining > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
			remaining--
		}
	}
	return nil
}
