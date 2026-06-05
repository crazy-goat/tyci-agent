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

	"github.com/decodo/tyci-agent/stream"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			ToolCalls []struct {
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
	InputTokens           int `json:"prompt_tokens"`
	InputTokensAlt        int `json:"input_tokens,omitempty"`
	OutputTokens          int `json:"completion_tokens"`
	OutputTokensAlt       int `json:"output_tokens,omitempty"`
	ReasoningTokens       int `json:"reasoning_tokens,omitempty"`
	CacheReadInputTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheCreateInputTokens int `json:"cache_creation_tokens,omitempty"`
}

func StreamChat(ctx context.Context, apiKey, endpoint string, body ChatRequest, emit func(stream.Event) error) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyStr := string(bodyBytes)
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

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			if readErr != nil {
				break
			}
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
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
			if delta.Reasoning != "" {
				if err := emit(stream.ThinkingDelta{Text: delta.Reasoning}); err != nil {
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
						Name      string
						Arguments strings.Builder
					}{})
				}
				if tc.Function.Name != "" {
					toolAcc[tc.Index].Name = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
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

	for _, tc := range toolAcc {
		if tc.Name == "" && tc.Arguments.Len() == 0 {
			continue
		}
		if err := emit(stream.ToolCall{Name: tc.Name, Arguments: tc.Arguments.String()}); err != nil {
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
