//go:build !nogemini

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

// GeminiClient implements the Streamer interface for Gemini APIs.
type GeminiClient struct {
	APIKey   string
	Endpoint string
}

// NewGeminiClient creates a new GeminiClient.
func NewGeminiClient(apiKey, endpoint string) *GeminiClient {
	return &GeminiClient{
		APIKey:   apiKey,
		Endpoint: endpoint,
	}
}

// Stream implements the Streamer interface for Gemini APIs.
func (c *GeminiClient) Stream(ctx context.Context, req *StreamRequest, emit EmitFunc) error {
	// Convert StreamRequest to GeminiRequest
	geminiReq := c.toGeminiRequest(req)

	jsonBody, err := json.Marshal(geminiReq)
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

// toGeminiRequest converts StreamRequest to GeminiRequest
func (c *GeminiClient) toGeminiRequest(req *StreamRequest) GeminiRequest {
	var contents []GeminiContent

	for _, msg := range req.Messages {
		// Skip system messages (handled separately)
		if msg.Role == "system" {
			continue
		}

		// Map roles: assistant -> model
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}

		var parts []GeminiPart

		// Add text content if present
		if msg.Content != "" {
			parts = append(parts, GeminiPart{
				Text: msg.Content,
			})
		}

		// Add tool calls if present (from assistant messages)
		for _, tc := range msg.ToolCalls {
			var args json.RawMessage
			if tc.Function.Arguments != "" {
				args = json.RawMessage(tc.Function.Arguments)
			}
			parts = append(parts, GeminiPart{
				FunctionCall: &GeminiFunctionCall{
					Name: tc.Function.Name,
					Args: args,
				},
			})
		}

		// Add function response if present (from tool results)
		if msg.ToolResult != nil {
			parts = append(parts, GeminiPart{
				FunctionResponse: &GeminiFunctionResponse{
					Name: msg.ToolCallID, // Use tool call ID as function name for matching
					Response: struct {
						Name    string `json:"name"`
						Content string `json:"content"`
					}{
						Name:    msg.ToolCallID,
						Content: msg.ToolResult.Content,
					},
				},
			})
		}

		if len(parts) > 0 {
			contents = append(contents, GeminiContent{
				Parts: parts,
				Role:  role,
			})
		}
	}

	// Build system instruction
	var systemInstruction *struct {
		Parts []GeminiPart `json:"parts"`
	}
	if req.System != "" {
		systemInstruction = &struct {
			Parts []GeminiPart `json:"parts"`
		}{
			Parts: []GeminiPart{{Text: req.System}},
		}
	}

	// Convert tools to Gemini format
	var tools []GeminiTools
	if len(req.Tools) > 0 {
		var declarations []GeminiToolDeclaration
		for _, tool := range req.Tools {
			var params json.RawMessage
			if tool.Function.Parameters != nil {
				params, _ = json.Marshal(tool.Function.Parameters)
			}
			declarations = append(declarations, GeminiToolDeclaration{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  params,
			})
		}
		tools = append(tools, GeminiTools{
			FunctionDeclarations: declarations,
		})
	}

	return GeminiRequest{
		Contents:          contents,
		Stream:            true,
		SystemInstruction: systemInstruction,
		Tools:             tools,
	}
}

// parseSSEStream parses the SSE stream from Gemini API
func (c *GeminiClient) parseSSEStream(ctx context.Context, body io.Reader, emit EmitFunc) error {
	dl := debug.FromContext(ctx)
	reader := bufio.NewReader(body)
	var inputTokens, outputTokens int
	var finishReason string
	var toolCalls []*pendingTool
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

		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if readErr != nil {
				break
			}
			continue
		}

		for _, c := range chunk.Candidates {
			if c.FinishReason != "" {
				finishReason = c.FinishReason
			}
			for _, part := range c.Content.Parts {
				if part.Text != "" {
					if err := emit(stream.TextDelta{Text: part.Text}); err != nil {
						return err
					}
				}
				if part.FunctionCall != nil && len(*part.FunctionCall) > 0 {
					var fc geminiFunctionCallArgs
					if err := json.Unmarshal(*part.FunctionCall, &fc); err != nil {
						continue
					}
					toolID := fmt.Sprintf("%s_%d", fc.Name, len(toolCalls))

					if err := emit(stream.ToolCallStart{ID: toolID, Name: fc.Name}); err != nil {
						return err
					}

					argsJSON := string(fc.Args)
					if argsJSON != "" {
						if err := emit(stream.ToolCallDelta{ID: toolID, Delta: argsJSON}); err != nil {
							return err
						}
					}

					if err := emit(stream.ToolCall{ID: toolID, Name: fc.Name, Arguments: argsJSON}); err != nil {
						return err
					}

					toolCalls = append(toolCalls, &pendingTool{
						id:   toolID,
						name: fc.Name,
					})
				}
			}
		}

		if chunk.UsageMetadata != nil {
			inputTokens = chunk.UsageMetadata.PromptTokenCount
			outputTokens = chunk.UsageMetadata.CandidatesTokenCount
		}

		if readErr != nil {
			break
		}
	}

	if finishReason == "" {
		finishReason = "stop"
	}

	if err := emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:  inputTokens,
			Output: outputTokens,
		},
	}); err != nil {
		return err
	}

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return nil
}

// pendingTool tracks tool calls for Gemini (which doesn't provide IDs)
type pendingTool struct {
	id   string
	name string
}
