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

type ResponsesMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

type ResponsesInput struct {
	Messages []ResponsesMessage `json:"messages"`
}

type ResponsesRequest struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Input  ResponsesInput  `json:"input"`
	Tools  json.RawMessage `json:"tools,omitempty"`
}

type responsesStreamChunk struct {
	Type   string `json:"type"`
	Output *struct {
		Messages []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages,omitempty"`
	} `json:"output,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func StreamResponses(ctx context.Context, apiKey, endpoint string, body ResponsesRequest, handler *DebugHandler) error {
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

		var chunk responsesStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Type == "response.output_text.delta" && chunk.Output != nil {
			for _, msg := range chunk.Output.Messages {
				for _, c := range msg.Content {
					if c.Type == "text" {
						handler.Chunk(c.Text)
					}
				}
			}
		}
		if chunk.Type == "response.done" && chunk.Usage != nil {
			handler.Summary(UsageInfo{
				InputTokens:  chunk.Usage.InputTokens,
				OutputTokens: chunk.Usage.OutputTokens,
			})
		}
	}

	handler.Summary(UsageInfo{})
	handler.End()
	return nil
}
