package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

type ToolRunner interface {
	Run(ctx context.Context, name string, args map[string]any) (string, error)
}

type Config struct {
	Model          string
	System         string
	MaxRetries     int
	MaxIterations  int // max tool-call iterations; -1 or 0 means unlimited
	Debug          bool
	Tools          ToolRunner
	Schema         json.RawMessage
	Session        *session.Session // optional session logging / resume
	ProviderName   string           // provider name for session metadata
	FallbackModels []string         // full "provider/model" strings for fallback
}

// Run executes the agent loop. It will make at most MaxRetries retries on
// transient errors, and at most MaxIterations tool-call iterations.
// If MaxIterations <= 0, there is no iteration limit.
// If cfg.FallbackModels is set, non-retryable errors will try fallback models
// before giving up. Once a fallback succeeds, it is used for the rest of the session.
// Returns total usage accumulated during the run.
func Run(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.RichMessage, cfg Config) (stream.Usage, error) {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}

	var totalUsage stream.Usage

	// Track fallback state across iterations
	fs := fallbackState{
		active:    false,
		idx:       -1,
		provider:  p,
		model:     cfg.Model,
		fullModel: "",
	}

	for iter := 0; cfg.MaxIterations <= 0 || iter < cfg.MaxIterations; iter++ {
		more, usage, err := runOnce(ctx, fs.provider, d, msgs, cfg.withModel(fs.model))
		if usage != nil {
			totalUsage.Add(*usage)
		}
		if err != nil {
			// Check for context cancellation first
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return totalUsage, err
			}

			// Try fallback models if available
			if len(cfg.FallbackModels) > 0 {
				fbMore, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
				if fbErr == nil {
					// Fallback succeeded — continue or exit based on fbMore
					if !fbMore {
						return totalUsage, nil
					}
					continue
				}
				// All fallbacks failed
				d.Error(fmt.Errorf("all fallback models exhausted: %v", fbErr))
				return totalUsage, fbErr
			}

			// No fallbacks — use retry logic for retryable errors
			if !api.IsRetryable(err) {
				d.Error(err)
				return totalUsage, err
			}

			var lastErr error = err
			recovered := false
			for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
				backoff := api.CalcBackoff(attempt, lastErr, api.RetryConfig{MaxRetries: cfg.MaxRetries})
				d.ToolBlock(fmt.Sprintf("retry %d/%d — %s", attempt+1, cfg.MaxRetries, lastErr.Error()))
				if err := sleepWithCountdown(ctx, backoff); err != nil {
					return totalUsage, err
				}
				more, usage, err = runOnce(ctx, fs.provider, d, msgs, cfg.withModel(fs.model))
				if usage != nil {
					totalUsage.Add(*usage)
				}
				if err == nil {
					recovered = true
					break
				}
				lastErr = err
				if !api.IsRetryable(err) {
					// Non-retryable during retry — try fallback if available
					if len(cfg.FallbackModels) > 0 {
						fbMore, fbErr := tryFallback(ctx, d, msgs, cfg, &fs, &totalUsage, err)
						if fbErr == nil {
							if !fbMore {
								return totalUsage, nil
							}
							recovered = true
							break
						}
						d.Error(fmt.Errorf("all fallback models exhausted: %v", fbErr))
						return totalUsage, fbErr
					}
					d.Error(err)
					return totalUsage, err
				}
			}
			if !recovered {
				d.Error(fmt.Errorf("all %d retries exhausted: %v", cfg.MaxRetries, lastErr))
				return totalUsage, lastErr
			}
		}
		if !more {
			return totalUsage, nil
		}
	}

	if cfg.MaxIterations > 0 {
		d.Text(fmt.Sprintf("\n⚠️ Agent executed %d tool-call iterations – possible infinite loop. Stopping.\n", cfg.MaxIterations))
	}
	return totalUsage, nil
}

// withModel returns a copy of Config with the given model name.
func (c Config) withModel(model string) Config {
	c.Model = model
	return c
}

// tryFallback attempts to switch to the next fallback model.
// It updates fs with the new provider/model on success.
// Returns (more, nil) on success, (false, err) if all fallbacks exhausted.
// origErr is the error that triggered the fallback.
