package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/decodo/tyci-agent/internal/debug"
	"github.com/decodo/tyci-agent/stream"
)

// AnthropicContentBlock is a generic content block for Anthropic messages.
type AnthropicContentBlock struct {
	Type   string `json:"type"` // "text", "tool_use", "tool_result"
	Text   string `json:"text,omitempty"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Input  any    `json:"input,omitempty"`

	// Tool result fields
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content,omitempty"`
	IsError bool `json:"is_error,omitempty"`
}

type AnthropicMessage struct {
	Role    string                 `json:"role"`
	Content []AnthropicContentBlock `json:"content"`
}

type AnthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
	System    string             `json:"system,omitempty"`
	Messages  []AnthropicMessage `json:"messages"`
	Tools     json.RawMessage    `json:"tools,omitempty"`
}

type anthropicStreamChunk struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func StreamAnthropic(ctx context.Context, apiKey, endpoint string, body AnthropicRequest, emit func(stream.Event) error) error {
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

	client := &http.Client{}
	resp, err := client.Do(req)
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
	var inputTokens, outputTokens int

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}

		if dl != nil {
			dl.WriteResponseLine([]byte(line))
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		processChunk := func(chunk anthropicStreamChunk) error {
			if chunk.Type == "content_block_delta" {
				if err := emit(stream.TextDelta{Text: chunk.Delta.Text}); err != nil {
					return err
				}
			}
			if chunk.Type == "message_delta" && chunk.Usage != nil {
				inputTokens = chunk.Usage.InputTokens
				outputTokens = chunk.Usage.OutputTokens
			}
			return nil
		}

		if strings.HasPrefix(data, "[") {
			var chunks []anthropicStreamChunk
			if err := json.Unmarshal([]byte(data), &chunks); err != nil {
				continue
			}
			for _, chunk := range chunks {
				if err := processChunk(chunk); err != nil {
					return err
				}
			}
			continue
		}

		var chunk anthropicStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if err := processChunk(chunk); err != nil {
			return err
		}
	}

	return emit(stream.Finish{
		Reason: "stop",
		Usage: stream.Usage{
			Input:  inputTokens,
			Output: outputTokens,
		},
	})
}
