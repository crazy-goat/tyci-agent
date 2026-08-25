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
	"github.com/decodo/tyci/stream"
)

type fallbackState struct {
	idx int // index into cfg.Fallbacks currently in use (-1 = primary)
	mc  connector.ModelClient
}

// tryFallback attempts to switch to the next fallback model. cfg.Fallbacks
// entries are already-resolved connector.ModelClient values — the caller
// resolved them (and reported any that failed to resolve) before calling
// Run, so there is no "not found" case here: only "the stream failed".
// It updates fs with the new client on success.
// Returns (more, roundUsage, nil) on success, (false, nil, err) if all
// fallbacks exhausted. origErr is the error that triggered the fallback.
// roundUsage is runOnce's own return (see its doc comment): nil when the
// round produced no usage, non-nil otherwise, so a caller tracking "usage of
// the last round" (agent.Run's context-budget accounting) doesn't lose track
// of it just because this turn happened to be served by a fallback model.
//
// Note: runOnce below is called with the SAME cfg the primary used, so
// cfg.Temperature (and every other request-shaping field) automatically
// carries over to the fallback model — there is no separate "fallback
// config" to keep in sync.
func tryFallback(ctx context.Context, d Sink, msgs *[]connector.Message, cfg Config, fs *fallbackState, totalUsage *stream.Usage, origErr error) (bool, *stream.Usage, error) {
	var lastErr error

	// Format the reason from the original error
	reason := formatFallbackReason(origErr)

	for fs.idx+1 < len(cfg.Fallbacks) {
		fs.idx++
		fb := cfg.Fallbacks[fs.idx]
		fbFull := connector.FullModel(fb)

		d.ToolBlock(fmt.Sprintf("Switching to fallback model: %s\nReason: %s", fbFull, reason))

		// Try the fallback
		more, roundUsage, _, err := runOnce(ctx, fb, d, msgs, cfg, totalUsage)
		if err != nil {
			lastErr = err
			d.ToolBlock(fmt.Sprintf("fallback %s also failed: %v", fbFull, err))
			continue
		}

		// Fallback succeeded — update state
		fs.mc = fb

		return more, roundUsage, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no fallback models available")
	}
	return false, nil, lastErr
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
