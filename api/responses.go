package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/decodo/tyci-agent/stream"
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

func StreamResponses(ctx context.Context, apiKey, endpoint string, body ResponsesRequest, emit func(stream.Event) error) error {
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
	var inputTokens, outputTokens int

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

		var chunk responsesStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Type == "response.output_text.delta" && chunk.Output != nil {
			for _, msg := range chunk.Output.Messages {
				for _, c := range msg.Content {
					if c.Type == "text" {
						if err := emit(stream.TextDelta{Text: c.Text}); err != nil {
							return err
						}
					}
				}
			}
		}
		if chunk.Type == "response.done" && chunk.Usage != nil {
			inputTokens = chunk.Usage.InputTokens
			outputTokens = chunk.Usage.OutputTokens
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
