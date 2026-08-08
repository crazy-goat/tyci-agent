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
// Returns (more, nil) on success, (false, err) if all fallbacks exhausted.
// origErr is the error that triggered the fallback.
func tryFallback(ctx context.Context, d Sink, msgs *[]connector.Message, cfg Config, fs *fallbackState, totalUsage *stream.Usage, origErr error) (bool, error) {
	var lastErr error

	// Format the reason from the original error
	reason := formatFallbackReason(origErr)

	for fs.idx+1 < len(cfg.Fallbacks) {
		fs.idx++
		fb := cfg.Fallbacks[fs.idx]
		fbFull := connector.FullModel(fb)

		d.ToolBlock(fmt.Sprintf("Switching to fallback model: %s\nReason: %s", fbFull, reason))

		// Try the fallback
		more, _, _, err := runOnce(ctx, fb, d, msgs, cfg, totalUsage)
		if err != nil {
			lastErr = err
			d.ToolBlock(fmt.Sprintf("fallback %s also failed: %v", fbFull, err))
			continue
		}

		// Fallback succeeded — update state
		fs.mc = fb

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
