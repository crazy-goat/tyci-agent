package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/decodo/tyci-agent/api"
	"github.com/decodo/tyci-agent/display"
	"github.com/decodo/tyci-agent/providers"
	"github.com/decodo/tyci-agent/session"
	"github.com/decodo/tyci-agent/stream"
)

type ToolRunner interface {
	Run(ctx context.Context, name string, args map[string]any) (string, error)
}

type Config struct {
	Model         string
	System        string
	MaxRetries    int
	MaxIterations int // max tool-call iterations; -1 or 0 means unlimited
	Debug         bool
	Tools         ToolRunner
	Schema        json.RawMessage
	Session       *session.Session // optional session logging / resume
	ProviderName  string           // provider name for session metadata
	FallbackModels []string        // full "provider/model" strings for fallback
}

// fallbackState tracks which fallback model we're currently using.
type fallbackState struct {
	active   bool   // true if we've switched to a fallback
	idx      int    // index into FallbackModels currently in use (-1 = primary)
	provider providers.Provider
	model    string
	fullModel string
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
			totalUsage.Input += usage.Input
			totalUsage.Output += usage.Output
			totalUsage.Reasoning += usage.Reasoning
			totalUsage.CacheRead += usage.CacheRead
			totalUsage.CacheWrite += usage.CacheWrite
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
				d.ToolBlock("all fallback models exhausted: " + fbErr.Error())
				return totalUsage, fbErr
			}

			// No fallbacks — use retry logic for retryable errors
			if !api.IsRetryable(err) {
				d.ToolBlock(err.Error())
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
					totalUsage.Input += usage.Input
					totalUsage.Output += usage.Output
					totalUsage.Reasoning += usage.Reasoning
					totalUsage.CacheRead += usage.CacheRead
					totalUsage.CacheWrite += usage.CacheWrite
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
						d.ToolBlock("all fallback models exhausted: " + fbErr.Error())
						return totalUsage, fbErr
					}
					d.ToolBlock(err.Error())
					return totalUsage, err
				}
			}
			if !recovered {
				d.ToolBlock(fmt.Sprintf("all %d retries exhausted: %v", cfg.MaxRetries, lastErr))
				return totalUsage, lastErr
			}
		}
		if !more {
			return totalUsage, nil
		}
	}

	if cfg.MaxIterations > 0 {
		d.Text(fmt.Sprintf("\n⚠️ Agent wykonał %d iteracji narzędzi – możliwa nieskończona pętla. Przerywam.\n", cfg.MaxIterations))
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
func tryFallback(ctx context.Context, d display.Display, msgs *[]providers.RichMessage, cfg Config, fs *fallbackState, totalUsage *stream.Usage, origErr error) (bool, error) {
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
		more, usage, err := runOnce(ctx, fbProvider, d, msgs, cfg.withModel(fbModel))
		if usage != nil {
			totalUsage.Input += usage.Input
			totalUsage.Output += usage.Output
			totalUsage.Reasoning += usage.Reasoning
			totalUsage.CacheRead += usage.CacheRead
			totalUsage.CacheWrite += usage.CacheWrite
		}
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

func runOnce(ctx context.Context, p providers.Provider, d display.Display, msgs *[]providers.RichMessage, cfg Config) (more bool, usage *stream.Usage, err error) {
	events, streamErr := p.Stream(ctx, providers.Request{
		Model:    cfg.Model,
		System:   cfg.System,
		Messages: *msgs,
		Tools:    cfg.Schema,
		Debug:    cfg.Debug,
	})
	if streamErr != nil {
		return false, nil, streamErr
	}

	var toolCalls []stream.ToolCall
	var toolDeltas = make(map[string]strings.Builder) // accumulate deltas per tool call ID
	var lastUsage stream.Usage
	var textBuf strings.Builder
	var thinkingBuf strings.Builder
	startTime := time.Now()
	var firstToken time.Duration
	var hasFirstToken bool

	var toolBlockShown bool

	for ev := range events {
		switch e := ev.(type) {
		case stream.ThinkingDelta:
			if !hasFirstToken {
				firstToken = time.Since(startTime)
				hasFirstToken = true
			}
			d.Thinking(e.Text)
			thinkingBuf.WriteString(e.Text)
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
			// Track for full arguments
			if _, ok := toolDeltas[e.ID]; !ok {
				toolDeltas[e.ID] = strings.Builder{}
			}
			// Gray box with hourglass on first tool detection – instant feedback
			if !toolBlockShown {
				d.ToolBlock("⏳ waiting for tools...")
				toolBlockShown = true
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
			return false, nil, e.Err
		}
	}

	hasText := textBuf.Len() > 0
	hasTools := len(toolCalls) > 0

	if hasText || hasTools {
		var content []providers.ContentBlock

		// Add thinking block first (before text, chronologically)
		if thinkingBuf.Len() > 0 {
			content = append(content, providers.ContentBlock{
				Type:     "thinking",
				Thinking: thinkingBuf.String(),
			})
		}

		// Add text block
		if hasText {
			content = append(content, providers.ContentBlock{
				Type: "text",
				Text: textBuf.String(),
			})
		}

		// Add tool call blocks
		for _, tc := range toolCalls {
			var args json.RawMessage
			if tc.Arguments != "" {
				args = json.RawMessage(tc.Arguments)
			}
			content = append(content, providers.ContentBlock{
				Type:      "toolCall",
				ID:        tc.ID,
				Name:      tc.Name,
				Arguments: args,
			})
		}

		msg := providers.RichMessage{
			Role:    "assistant",
			Content: content,
		}
		*msgs = append(*msgs, msg)

		// Write assistant message to session
		if cfg.Session != nil {
			writeAssistantSessionEvent(cfg.Session, p.Name(), cfg.Model, msg, &lastUsage)
		}
	}

	if !hasTools {
		// No tools – show usage and stop
		if lastUsage.Input > 0 || lastUsage.Output > 0 {
			d.Summary(lastUsage, stream.Stats{
				Duration:   time.Since(startTime),
				FirstToken: firstToken,
			})
		}
		return false, &lastUsage, nil
	}

	// Show each tool call with its arguments
	for _, tc := range toolCalls {
		d.ToolCallStart(tc.Name)
		if delta, ok := toolDeltas[tc.ID]; ok && delta.Len() > 0 {
			d.ToolCallDelta(delta.String())
		} else if tc.Arguments != "" {
			d.ToolCallDelta(tc.Arguments)
		}
	}

	// Execute tools in parallel
	// Set up streaming callback if display supports it
	// Save previous to restore after nested runOnce calls (e.g. subagent)
	type streamer interface {
		StreamProgress(toolIdx int, line string)
	}
	prevOnOutput := stream.OnOutput
	if s, ok := d.(streamer); ok {
		stream.OnOutput = func(toolIdx int, line string) {
			s.StreamProgress(toolIdx, line)
		}
	} else {
		stream.OnOutput = nil
	}

	results := executeTools(ctx, cfg.Tools, toolCalls)

	// Restore previous streaming callback so nested calls don't permanently
	// overwrite the parent's callback (e.g. subagent inside agent loop).
	stream.OnOutput = prevOnOutput

	// Show results and write session events
	for i, tc := range toolCalls {
		d.ToolCallEnd(tc.Name, results[i])
		*msgs = append(*msgs, providers.RichMessage{
			Role: "toolResult",
			Content: []providers.ContentBlock{
				{
					Type:       "text",
					Text:       results[i],
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					IsError:    strings.HasPrefix(results[i], "Error:"),
				},
			},
		})

		// Write tool result to session
		if cfg.Session != nil {
			writeToolResultSessionEvent(cfg.Session, tc.ID, tc.Name, results[i])
		}
	}

	// Show usage AFTER tools execution
	if lastUsage.Input > 0 || lastUsage.Output > 0 {
		d.Summary(lastUsage, stream.Stats{
			Duration:   time.Since(startTime),
			FirstToken: firstToken,
		})
	}
	return true, &lastUsage, nil
}

func writeAssistantSessionEvent(s *session.Session, providerName, model string, msg providers.RichMessage, usage *stream.Usage) {
	// Convert providers.ContentBlock to session.ContentBlock (they have identical structure)
	blocks := make([]session.ContentBlock, len(msg.Content))
	for i, cb := range msg.Content {
		blocks[i] = session.ContentBlock{
			Type:       cb.Type,
			Text:       cb.Text,
			Thinking:    cb.Thinking,
			ID:         cb.ID,
			Name:       cb.Name,
			Arguments:  cb.Arguments,
			IsError:    cb.IsError,
			ToolCallID: cb.ToolCallID,
			ToolName:   cb.ToolName,
		}
	}

	opts := &session.MessageOptions{
		Provider: providerName,
		Model:    model,
	}
	if usage != nil {
		opts.Usage = &session.Usage{
			Input:       usage.Input,
			Output:      usage.Output,
			Reasoning:   usage.Reasoning,
			CacheRead:   usage.CacheRead,
			CacheWrite:  usage.CacheWrite,
			TotalTokens: usage.Input + usage.Output + usage.Reasoning,
		}
	}

	if err := s.WriteMessage("assistant", blocks, opts); err != nil {
		// Non-fatal: log but don't break agent
		fmt.Fprintf(os.Stderr, "Warning: session write (assistant): %v\n", err)
	}
}

func writeToolResultSessionEvent(s *session.Session, toolCallID, toolName, result string) {
	blocks := []session.ContentBlock{
		{
			Type:       "text",
			Text:       result,
			ToolCallID: toolCallID,
			ToolName:   toolName,
			IsError:    strings.HasPrefix(result, "Error:"),
		},
	}
	if err := s.WriteMessage("toolResult", blocks, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session write (toolResult): %v\n", err)
	}
}

// WriteSessionEnd writes a session_end event and closes the session.
// Safe to call multiple times; subsequent calls are no-ops.
func WriteSessionEnd(s *session.Session, status string, exitCode int, totalUsage *stream.Usage) {
	if s == nil {
		return
	}
	var u *session.Usage
	if totalUsage != nil {
		u = &session.Usage{
			Input:       totalUsage.Input,
			Output:      totalUsage.Output,
			Reasoning:   totalUsage.Reasoning,
			CacheRead:   totalUsage.CacheRead,
			CacheWrite:  totalUsage.CacheWrite,
			TotalTokens: totalUsage.Input + totalUsage.Output + totalUsage.Reasoning,
		}
	}
	if err := s.WriteSessionEnd(status, exitCode, u); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session write (session_end): %v\n", err)
	}
	if err := s.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session close: %v\n", err)
	}
}

func executeTools(ctx context.Context, runner ToolRunner, toolCalls []stream.ToolCall) []string {
	results := make([]string, len(toolCalls))
	var wg sync.WaitGroup

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, call stream.ToolCall) {
			defer wg.Done()

			var args map[string]any
			if call.Arguments != "" {
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					results[idx] = "Error: invalid arguments: " + err.Error()
					return
				}
			}
			if args == nil {
				args = make(map[string]any)
			}

			// Determine timeout per tool type
			var toolTimeout time.Duration
			switch call.Name {
			case "read", "write", "edit":
				toolTimeout = 30 * time.Second
			case "bash":
				toolTimeout = 120 * time.Second // default
				if to, ok := args["timeout"]; ok {
					switch v := to.(type) {
					case float64:
						toolTimeout = time.Duration(v) * time.Second
					case int:
						toolTimeout = time.Duration(v) * time.Second
					}
				}
			case "subagent":
				toolTimeout = 0 // no timeout — subagent has its own internal timeout
			default:
				toolTimeout = 60 * time.Second
			}

			// Create tool-specific context with timeout (if set)
			toolCtx := ctx
			var cancel context.CancelFunc
			if toolTimeout > 0 {
				toolCtx, cancel = context.WithTimeout(ctx, toolTimeout)
				defer cancel()
			}

			// Pass tool index for streaming tools (bash, subagent)
			if call.Name == "bash" || call.Name == "subagent" {
				toolCtx = context.WithValue(toolCtx, stream.ToolIdxCtxKey{}, idx)
			}

			body, err := runner.Run(toolCtx, call.Name, args)
			if err != nil {
				// Check the actual context state – the returned error may have lost its type
				// after passing through tool wrappers (fmt.Errorf etc.).
				if toolCtx.Err() == context.DeadlineExceeded {
					results[idx] = fmt.Sprintf("Error: %s tool timed out after %v", call.Name, toolTimeout)
				} else {
					results[idx] = "Error: " + err.Error()
				}
			} else {
				results[idx] = body
			}
		}(i, tc)
	}

	wg.Wait()
	return results
}
