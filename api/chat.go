package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages []ChatMessage   `json:"messages"`
	Tools    json.RawMessage `json:"tools,omitempty"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning"`
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
}

func StreamChat(ctx context.Context, apiKey, endpoint string, body ChatRequest, handler *DebugHandler) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	handler.LogRequest("POST", endpoint, body)

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
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(bodyBytes))
	}

	reader := bufio.NewReader(resp.Body)
	var sawThinking bool
	var toolStarted bool
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		handler.LogResponse(data)

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]
			delta := choice.Delta
			if delta.Reasoning != "" {
				handler.Thinking(delta.Reasoning)
				sawThinking = true
			}
			if delta.Content != "" {
				if sawThinking {
					handler.EndThinking()
					sawThinking = false
				}
				handler.Chunk(delta.Content)
			}
			for _, tc := range delta.ToolCalls {
				if sawThinking {
					handler.EndThinking()
					sawThinking = false
				}
				if tc.Function.Name != "" {
					if toolStarted {
						handler.EndToolCall()
					}
					handler.LogToolCallStart(tc.Function.Name)
					toolStarted = true
				}
				if tc.Function.Arguments != "" {
					handler.ToolCallArg(tc.Function.Arguments)
				}
				handler.AccumulateToolCall(tc.Index, tc.Function.Name, tc.Function.Arguments)
			}
			if choice.FinishReason == "tool_calls" {
				if toolStarted {
					handler.EndToolCall()
					toolStarted = false
				}
			}
		}
	}

	handler.Summary(UsageInfo{})
	handler.End()
	return nil
}
