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

// ChatClient implements the Streamer interface for OpenAI-compatible APIs.
type ChatClient struct {
	APIKey   string
	Endpoint string
}

// NewChatClient creates a new ChatClient.
func NewChatClient(apiKey, endpoint string) *ChatClient {
	return &ChatClient{
		APIKey:   apiKey,
		Endpoint: endpoint,
	}
}

// Stream implements the Streamer interface for OpenAI/Chat APIs.
func (c *ChatClient) Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error {
	// Convert StreamRequest to ChatRequest
	chatReq := c.toChatRequest(req)

	jsonBody, err := json.Marshal(chatReq)
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

// toChatRequest converts StreamRequest to ChatRequest
func (c *ChatClient) toChatRequest(req *StreamRequest) ChatRequest {
	messages := make([]ChatMessage, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = ChatMessage{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		if len(msg.ToolCalls) > 0 {
			messages[i].ToolCalls = make([]ChatToolCall, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				messages[i].ToolCalls[j] = ChatToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: ChatFunctionCall{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}
		}
	}

	// Convert tools to JSON
	var toolsJSON json.RawMessage
	if len(req.Tools) > 0 {
		toolsJSON, _ = json.Marshal(req.Tools)
	}

	return ChatRequest{
		Model:     req.Model,
		Stream:    true,
		Messages:  messages,
		Tools:     toolsJSON,
		Reasoning: req.Temperature == 0, // Enable reasoning for temperature 0
	}
}

// parseSSEStream parses the SSE stream from OpenAI/Chat API
func (c *ChatClient) parseSSEStream(ctx context.Context, body io.Reader, emit EmitFunc) error {
	dl := debug.FromContext(ctx)
	reader := bufio.NewReader(body)
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

	for _, tc := range toolAcc {
		if tc.Name == "" && tc.Arguments.Len() == 0 {
			continue
		}
		if err := emit(stream.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments.String()}); err != nil {
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
