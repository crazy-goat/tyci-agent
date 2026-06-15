//go:build !nogemini

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/stream"
)

// geminiPartRaw is used for unmarshalling SSE parts with unknown structure.
type geminiPartRaw struct {
	Text         string           `json:"text,omitempty"`
	FunctionCall *json.RawMessage `json:"functionCall,omitempty"`
}

// geminiStreamChunk represents a single SSE event from the Gemini API.
type geminiStreamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPartRaw `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

// geminiFunctionCallArgs holds the parsed function call from the model.
type geminiFunctionCallArgs struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

func StreamGemini(ctx context.Context, apiKey, endpoint string, body GeminiRequest, emit func(stream.Event) error) error {
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
	var inputTokens, outputTokens int
	var finishReason string
	// toolCalls tracks tool call IDs for generating unique IDs
	type pendingTool struct {
		id   string
		name string
	}
	var toolCalls []*pendingTool

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

		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
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
					// Generate a unique tool call ID (Gemini doesn't provide one)
					toolID := fmt.Sprintf("%s_%d", fc.Name, len(toolCalls))

					// Emit start event
					if err := emit(stream.ToolCallStart{ID: toolID, Name: fc.Name}); err != nil {
						return err
					}

					// Emit the full arguments as a single delta
					argsJSON := string(fc.Args)
					if argsJSON != "" {
						if err := emit(stream.ToolCallDelta{ID: toolID, Delta: argsJSON}); err != nil {
							return err
						}
					}

					// Emit the complete tool call
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
	}

	if finishReason == "" {
		finishReason = "stop"
	}

	return emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:  inputTokens,
			Output: outputTokens,
		},
	})
}
