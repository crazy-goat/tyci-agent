//go:build !noanthropic

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

// anthropicMessageUsage captures the usage inside a message_start event.
type anthropicMessageUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// anthropicMessage captures the message object inside message_start events.
type anthropicMessage struct {
	Usage *anthropicMessageUsage `json:"usage,omitempty"`
}

type anthropicStreamChunk struct {
	Type string `json:"type"`
	// Message is present in message_start events.
	Message *anthropicMessage `json:"message,omitempty"`
	Delta   struct {
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

	resp, err := ClientFromContext(ctx).Do(req)
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
	var inputTokens, outputTokens, cacheRead, cacheWrite int

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

		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimLeft(data, " \t")
		if data == "[DONE]" {
			break
		}

		processChunk := func(chunk anthropicStreamChunk) error {
			switch chunk.Type {
			case "message_start":
				if chunk.Message != nil && chunk.Message.Usage != nil {
					inputTokens = chunk.Message.Usage.InputTokens
					cacheRead = chunk.Message.Usage.CacheReadInputTokens
					cacheWrite = chunk.Message.Usage.CacheCreationInputTokens
				}
			case "content_block_delta":
				if err := emit(stream.TextDelta{Text: chunk.Delta.Text}); err != nil {
					return err
				}
			case "message_delta":
				if chunk.Usage != nil {
					outputTokens = chunk.Usage.OutputTokens
				}
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
			Input:      inputTokens,
			Output:     outputTokens,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
		},
	})
}
