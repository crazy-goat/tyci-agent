package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/stream"
)

type ToolRunner interface {
	Run(ctx context.Context, name string, args map[string]any) (string, error)
}

type Config struct {
	Model      string
	System     string
	MaxRetries int
	Debug      bool
	Tools      ToolRunner
	Schema     json.RawMessage
}

const DefaultMaxIterations = 50

// Run executes the agent loop. It will make at most MaxRetries retries on
// transient errors, and at most MaxIterations tool-call iterations.
// If MaxIterations is 0, DefaultMaxIterations is used.
func Run(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.Message, cfg Config) error {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 5
	}

	maxIter := DefaultMaxIterations

	for iter := 0; iter < maxIter; iter++ {
		more, err := runOnce(ctx, p, d, msgs, cfg)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if !api.IsRetryable(err) {
				d.Error(err)
				return err
			}

			var lastErr error = err
			recovered := false
			for attempt := 0; attempt < cfg.MaxRetries; attempt++ {
				backoff := api.CalcBackoff(attempt, lastErr, api.RetryConfig{MaxRetries: cfg.MaxRetries})
				d.Error(fmt.Errorf("⟳ retry %d/%d — %s", attempt+1, cfg.MaxRetries, lastErr.Error()))
				if err := sleepWithCountdown(ctx, backoff); err != nil {
					return err
				}
				more, err = runOnce(ctx, p, d, msgs, cfg)
				if err == nil {
					recovered = true
					break
				}
				lastErr = err
				if !api.IsRetryable(err) {
					d.Error(err)
					return err
				}
			}
			if !recovered {
				d.Error(fmt.Errorf("all %d retries exhausted: %w", cfg.MaxRetries, lastErr))
				return lastErr
			}
		}
		if !more {
			return nil
		}
	}

	d.Text(fmt.Sprintf("\n⚠️ Agent wykonał %d iteracji narzędzi – możliwa nieskończona pętla. Przerywam.\n", maxIter))
	return nil
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

func runOnce(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.Message, cfg Config) (more bool, err error) {
	events, streamErr := p.Stream(ctx, providers.Request{
		Model:    cfg.Model,
		System:   cfg.System,
		Messages: *msgs,
		Tools:    cfg.Schema,
		Debug:    cfg.Debug,
	})
	if streamErr != nil {
		return false, streamErr
	}

	var toolCalls []stream.ToolCall
	var toolDeltas = make(map[string]strings.Builder) // accumulate deltas per tool call ID
	var lastUsage stream.Usage
	var textBuf strings.Builder
	startTime := time.Now()
	var firstToken time.Duration
	var hasFirstToken bool

	for ev := range events {
		switch e := ev.(type) {
		case stream.ThinkingDelta:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			d.Thinking(e.Text)
		case stream.TextDelta:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			d.Text(e.Text)
			textBuf.WriteString(e.Text)
		case stream.ToolCallStart:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			// Just track that we received this tool call, don't display yet
			if _, ok := toolDeltas[e.ID]; !ok {
				toolDeltas[e.ID] = strings.Builder{}
			}
		case stream.ToolCallDelta:
			if b, ok := toolDeltas[e.ID]; ok {
				b.WriteString(e.Delta)
			} else {
				var sb strings.Builder
				sb.WriteString(e.Delta)
				toolDeltas[e.ID] = sb
			}
		case stream.ToolCall:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			toolCalls = append(toolCalls, e)
		case stream.Finish:
			lastUsage = e.Usage
		case stream.StreamError:
			return false, e.Err
		}
	}

	if textBuf.Len() > 0 || len(toolCalls) > 0 {
		msg := providers.Message{Role: "assistant"}
		if textBuf.Len() > 0 {
			msg.Content = textBuf.String()
		}
		if len(toolCalls) > 0 {
			tcs := make([]stream.ToolCall, len(toolCalls))
			copy(tcs, toolCalls)
			msg.ToolCalls = tcs
		}
		*msgs = append(*msgs, msg)
	}

	// Show usage BEFORE tool calls
	if lastUsage.Input > 0 || lastUsage.Output > 0 {
		d.Summary(lastUsage, stream.Stats{
			Duration:   time.Since(startTime),
			FirstToken: firstToken,
		})
	}

	if len(toolCalls) == 0 {
		return false, nil
	}

	// Now display all tool calls (both streamed and non-streamed)
	for _, tc := range toolCalls {
		d.ToolCallStart(tc.Name)
		if delta, ok := toolDeltas[tc.ID]; ok && delta.Len() > 0 {
			d.ToolCallDelta(delta.String())
		} else if tc.Arguments != "" {
			d.ToolCallDelta(tc.Arguments)
		}
	}

	results := executeTools(ctx, cfg.Tools, toolCalls)
	for i, tc := range toolCalls {
		d.ToolCallEnd(tc.Name, results[i])
		*msgs = append(*msgs, providers.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Content:    results[i],
		})
	}
	return true, nil
}

func executeTools(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall) []string {
	results := make([]string, len(toolCalls))
	for i, tc := range toolCalls {
		var args map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				results[i] = "Error: invalid arguments: " + err.Error()
				continue
			}
		}
		body, err := runner.Run(ctx, tc.Name, args)
		if err != nil {
			results[i] = "Error: " + err.Error()
		} else {
			results[i] = body
		}
	}
	return results
}
