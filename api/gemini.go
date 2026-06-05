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

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiRequest struct {
	Contents          []GeminiContent `json:"contents"`
	Stream            bool            `json:"stream"`
	SystemInstruction *struct {
		Parts []GeminiPart `json:"parts"`
	} `json:"systemInstruction,omitempty"`
}

type geminiStreamChunk struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata *struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata,omitempty"`
}

func StreamGemini(ctx context.Context, apiKey, endpoint string, body GeminiRequest, emit func(stream.Event) error) error {
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

		var chunk geminiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Candidates {
			for _, part := range c.Content.Parts {
				if err := emit(stream.TextDelta{Text: part.Text}); err != nil {
					return err
				}
			}
		}
		if chunk.UsageMetadata != nil {
			inputTokens = chunk.UsageMetadata.PromptTokenCount
			outputTokens = chunk.UsageMetadata.CandidatesTokenCount
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
