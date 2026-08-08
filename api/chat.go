package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/stream"
)

type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
}

type ChatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ChatFunctionCall `json:"function"`
}

type ChatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatRequest struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	Messages  []ChatMessage   `json:"messages"`
	Tools     json.RawMessage `json:"tools,omitempty"`
	Reasoning bool            `json:"reasoning,omitempty"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content      string `json:"content"`
			Reasoning    string `json:"reasoning_content"`
			ReasoningAlt string `json:"reasoning"`
			ToolCalls    []struct {
				Type     string `json:"type"`
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage,omitempty"`
}

type chatUsage struct {
	InputTokens            int `json:"prompt_tokens"`
	InputTokensAlt         int `json:"input_tokens,omitempty"`
	OutputTokens           int `json:"completion_tokens"`
	OutputTokensAlt        int `json:"output_tokens,omitempty"`
	ReasoningTokens        int `json:"reasoning_tokens,omitempty"`
	CacheReadInputTokens   int `json:"cache_read_input_tokens,omitempty"`
	CacheCreateInputTokens int `json:"cache_creation_tokens,omitempty"`
}

func (u *chatUsage) UnmarshalJSON(data []byte) error {
	type alias chatUsage
	if err := json.Unmarshal(data, (*alias)(u)); err != nil {
		return err
	}
	if u.CacheReadInputTokens > 0 && u.CacheCreateInputTokens > 0 && u.ReasoningTokens > 0 {
		return nil
	}
	var extra struct {
		PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
		PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
		CachedTokens          int `json:"cached_tokens"`
		PromptTokensDetails   *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(data, &extra); err != nil {
		return nil
	}
	if u.CacheReadInputTokens == 0 {
		switch {
		case extra.PromptCacheHitTokens > 0:
			u.CacheReadInputTokens = extra.PromptCacheHitTokens
		case extra.CachedTokens > 0:
			u.CacheReadInputTokens = extra.CachedTokens
		case extra.PromptTokensDetails != nil && extra.PromptTokensDetails.CachedTokens > 0:
			u.CacheReadInputTokens = extra.PromptTokensDetails.CachedTokens
		}
	}
	if u.CacheCreateInputTokens == 0 && extra.PromptCacheMissTokens > 0 {
		u.CacheCreateInputTokens = extra.PromptCacheMissTokens
	}
	if u.ReasoningTokens == 0 && extra.CompletionTokensDetails != nil {
		u.ReasoningTokens = extra.CompletionTokensDetails.Reasoning
	}
	return nil
}

// ChatStreamer streams a request against the OpenAI chat-completions
// protocol. The zero value is usable and behaves exactly like the old
// package-level StreamChat function.
type ChatStreamer struct {
	// HTTP is the client to send with. nil means "resolve from the context"
	// (see doer); it is the seam the connector layer injects through.
	HTTP HTTPDoer
	// Headers are extra request headers, applied after the protocol defaults.
	Headers map[string]string
}

// Stream POSTs body to endpoint and calls emit for every decoded SSE event.
func (s ChatStreamer) Stream(ctx context.Context, apiKey, endpoint string, body ChatRequest, emit func(stream.Event) error) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	dl := debug.FromContext(ctx)
	if dl != nil {
		dl.WriteRequest("POST", endpoint, jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	applyExtraHeaders(req, s.Headers)

	resp, err := doer(ctx, s.HTTP).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyStr := string(bodyBytes)
		if dl != nil {
			dl.WriteResponse(resp.StatusCode, bodyBytes)
		}
		if resp.StatusCode == 429 {
			return &RetryableError{Code: 429, RetryAfter: resp.Header.Get("Retry-After"), Message: fmt.Sprintf("429 rate limited: %s", bodyStr)}
		}
		if resp.StatusCode >= 500 {
			return &RetryableError{Code: resp.StatusCode, Message: fmt.Sprintf("%d server error: %s", resp.StatusCode, bodyStr)}
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, bodyStr)
	}

	reader := bufio.NewReader(resp.Body)
	var toolAcc []struct {
		ID        string
		Name      string
		Arguments strings.Builder
	}
	var inputTokens, outputTokens, reasoningTokens, cacheRead, cacheWrite int
	var finishReason string
	var readErr error

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			readErr = err
			if line == "" {
				break
			}
		}

		if dl != nil {
			dl.WriteResponseLine([]byte(line))
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			if readErr != nil {
				break
			}
			continue
		}

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimLeft(data, " \t")
		if data == "[DONE]" {
			break
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if readErr != nil {
				break
			}
			continue
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			delta := choice.Delta
			reasoningText := delta.Reasoning
			if reasoningText == "" {
				reasoningText = delta.ReasoningAlt
			}
			if reasoningText != "" {
				if err := emit(stream.ThinkingDelta{Text: reasoningText}); err != nil {
					return err
				}
			}
			if delta.Content != "" {
				if err := emit(stream.TextDelta{Text: delta.Content}); err != nil {
					return err
				}
			}
			for _, tc := range delta.ToolCalls {
				for len(toolAcc) <= tc.Index {
					toolAcc = append(toolAcc, struct {
						ID        string
						Name      string
						Arguments strings.Builder
					}{})
				}
				isNew := toolAcc[tc.Index].ID == ""
				if tc.ID != "" {
					toolAcc[tc.Index].ID = tc.ID
				}
				if tc.Function.Name != "" {
					toolAcc[tc.Index].Name = tc.Function.Name
				}
				if isNew && toolAcc[tc.Index].ID != "" {
					if err := emit(stream.ToolCallStart{ID: toolAcc[tc.Index].ID, Name: toolAcc[tc.Index].Name}); err != nil {
						return err
					}
				}
				if tc.Function.Arguments != "" {
					if err := emit(stream.ToolCallDelta{ID: toolAcc[tc.Index].ID, Delta: tc.Function.Arguments}); err != nil {
						return err
					}
					toolAcc[tc.Index].Arguments.WriteString(tc.Function.Arguments)
				}
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			inputTokens = chunk.Usage.InputTokens
			if inputTokens == 0 {
				inputTokens = chunk.Usage.InputTokensAlt
			}
			outputTokens = chunk.Usage.OutputTokens
			if outputTokens == 0 {
				outputTokens = chunk.Usage.OutputTokensAlt
			}
			reasoningTokens = chunk.Usage.ReasoningTokens
			cacheRead = chunk.Usage.CacheReadInputTokens
			cacheWrite = chunk.Usage.CacheCreateInputTokens
		}

		if readErr != nil {
			break
		}
	}

	// Emit accumulated tool calls. Skip malformed entries: a tool call
	// without a name cannot be dispatched and triggers
	// "tool_calls[0] is missing a function name" 400s on strict providers,
	// and an empty ID later causes "missing field `tool_call_id`" on the
	// matching tool-result message. We generate a stable ID when the
	// provider returned none so the tool result can be paired back.
	for i, tc := range toolAcc {
		if tc.Name == "" {
			continue
		}
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", i)
		}
		if err := emit(stream.ToolCall{ID: id, Name: tc.Name, Arguments: tc.Arguments.String()}); err != nil {
			return err
		}
	}

	if err := emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:      inputTokens,
			Output:     outputTokens,
			Reasoning:  reasoningTokens,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
		},
	}); err != nil {
		return err
	}

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return nil
}
