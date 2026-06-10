//go:build !noanthropic

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

	"github.com/decodo/tyci-agent/internal/debug"
	"github.com/decodo/tyci-agent/stream"
)

// AnthropicClient implements the Streamer interface for Anthropic APIs.
type AnthropicClient struct {
	APIKey   string
	Endpoint string
}

// NewAnthropicClient creates a new AnthropicClient.
func NewAnthropicClient(apiKey, endpoint string) *AnthropicClient {
	return &AnthropicClient{
		APIKey:   apiKey,
		Endpoint: endpoint,
	}
}

// Stream implements the Streamer interface for Anthropic APIs.
func (c *AnthropicClient) Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error {
	// Convert StreamRequest to AnthropicRequest
	anthropicReq := c.toAnthropicRequest(req)

	jsonBody, err := json.Marshal(anthropicReq)
	if err != nil {
		return err
	}

	dl := debug.FromContext(ctx)
	if dl != nil {
		dl.WriteRequest("POST", c.Endpoint, jsonBody)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := ClientFromContext(ctx).Do(httpReq)
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

	return c.parseSSEStream(ctx, resp.Body, emit)
}

// toAnthropicRequest converts StreamRequest to AnthropicRequest
func (c *AnthropicClient) toAnthropicRequest(req *StreamRequest) AnthropicRequest {
	messages := make([]AnthropicMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		// Skip system messages (handled separately)
		if msg.Role == "system" {
			continue
		}

		anthropicMsg := AnthropicMessage{
			Role: msg.Role,
		}

		// Build content blocks
		var blocks []AnthropicContentBlock

		// Add text content if present
		if msg.Content != "" {
			blocks = append(blocks, AnthropicContentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}

		// Add tool calls if present
		for _, tc := range msg.ToolCalls {
			blocks = append(blocks, AnthropicContentBlock{
				Type: "tool_use",
				ID:   tc.ID,
				Name: tc.Function.Name,
				Input: func() json.RawMessage {
					if tc.Function.Arguments != "" {
						return json.RawMessage(tc.Function.Arguments)
					}
					return json.RawMessage("{}")
				}(),
			})
		}

		// Add tool result if present
		if msg.ToolResult != nil {
			content := []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				{Type: "text", Text: msg.ToolResult.Content},
			}
			blocks = append(blocks, AnthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   content,
				IsError:   msg.ToolResult.Error != "",
			})
		}

		// Only add message if we have content
		if len(blocks) > 0 {
			anthropicMsg.Content = blocks
			messages = append(messages, anthropicMsg)
		}
	}

	// Convert tools to Anthropic format
	var toolsJSON json.RawMessage
	if len(req.Tools) > 0 {
		toolsJSON, _ = json.Marshal(req.Tools)
		toolsJSON = ConvertToolsToAnthropic(toolsJSON)
	}

	return AnthropicRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    true,
		System:    req.System,
		Messages:  messages,
		Tools:     toolsJSON,
	}
}

// parseSSEStream parses the SSE stream from Anthropic API
func (c *AnthropicClient) parseSSEStream(ctx context.Context, body io.Reader, emit EmitFunc) error {
	dl := debug.FromContext(ctx)
	reader := bufio.NewReader(body)
	var inputTokens, outputTokens, cacheRead, cacheWrite int
	var finishReason string
	toolAcc := make(map[int]*toolAccumulator)
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

		processChunk := func(chunk anthropicStreamChunk) error {
			switch chunk.Type {
			case "message_start":
				if chunk.Message != nil && chunk.Message.Usage != nil {
					inputTokens = chunk.Message.Usage.InputTokens
					cacheRead = chunk.Message.Usage.CacheReadInputTokens
					cacheWrite = chunk.Message.Usage.CacheCreationInputTokens
				}

			case "content_block_start":
				if chunk.ContentBlock == nil {
					return nil
				}
				cb := chunk.ContentBlock
				switch cb.Type {
				case "text":
					if cb.Text != "" {
						if err := emit(stream.TextDelta{Text: cb.Text}); err != nil {
							return err
						}
					}
				case "tool_use":
					acc := &toolAccumulator{
						ID:   cb.ID,
						Name: cb.Name,
					}
					if cb.Input != nil {
						if argsJSON, err := json.Marshal(cb.Input); err == nil && len(argsJSON) > 2 {
							acc.Arguments.Write(argsJSON)
						}
					}
					toolAcc[chunk.Index] = acc
					if err := emit(stream.ToolCallStart{ID: acc.ID, Name: acc.Name}); err != nil {
						return err
					}
					if acc.Arguments.Len() > 0 {
						if err := emit(stream.ToolCallDelta{ID: acc.ID, Delta: acc.Arguments.String()}); err != nil {
							return err
						}
					}
				}

			case "content_block_delta":
				if len(chunk.Delta) == 0 {
					return nil
				}
				var d anthropicDelta
				if err := json.Unmarshal(chunk.Delta, &d); err != nil {
					return fmt.Errorf("failed to parse content_block_delta: %w", err)
				}
				switch d.Type {
				case "text_delta":
					if d.Text != "" {
						if err := emit(stream.TextDelta{Text: d.Text}); err != nil {
							return err
						}
					}
				case "input_json_delta":
					if d.PartialJSON == "" {
						return nil
					}
					if acc, ok := toolAcc[chunk.Index]; ok {
						acc.Arguments.WriteString(d.PartialJSON)
						if err := emit(stream.ToolCallDelta{ID: acc.ID, Delta: d.PartialJSON}); err != nil {
							return err
						}
					}
				}

			case "content_block_stop":
				if acc, ok := toolAcc[chunk.Index]; ok {
					if err := emit(stream.ToolCall{
						ID:        acc.ID,
						Name:      acc.Name,
						Arguments: acc.Arguments.String(),
					}); err != nil {
						return err
					}
					delete(toolAcc, chunk.Index)
				}

			case "message_delta":
				if chunk.Usage != nil {
					outputTokens = chunk.Usage.OutputTokens
				}
				if len(chunk.Delta) > 0 {
					var md anthropicMessageDeltaStop
					if err := json.Unmarshal(chunk.Delta, &md); err == nil && md.StopReason != "" {
						finishReason = md.StopReason
					}
				}
			}
			return nil
		}

		if strings.HasPrefix(data, "[") {
			var chunks []anthropicStreamChunk
			if err := json.Unmarshal([]byte(data), &chunks); err != nil {
				if readErr != nil {
					break
				}
				continue
			}
			for _, chunk := range chunks {
				if err := processChunk(chunk); err != nil {
					return err
				}
			}
			if readErr != nil {
				break
			}
			continue
		}

		var chunk anthropicStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if readErr != nil {
				break
			}
			continue
		}
		if err := processChunk(chunk); err != nil {
			return err
		}

		if readErr != nil {
			break
		}
	}

	for _, acc := range toolAcc {
		if err := emit(stream.ToolCall{
			ID:        acc.ID,
			Name:      acc.Name,
			Arguments: acc.Arguments.String(),
		}); err != nil {
			return err
		}
	}

	if finishReason == "" {
		finishReason = "stop"
	}
	if err := emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:      inputTokens,
			Output:     outputTokens,
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
