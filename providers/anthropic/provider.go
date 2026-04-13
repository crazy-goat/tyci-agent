package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/providers"
)

const baseURL = "https://opencode.ai/zen/go"

type provider struct{}

func init() {
	providers.Register(&provider{})
}

func (p *provider) Name() string {
	return "anthropic"
}

func (p *provider) IsConfigured() bool {
	key := os.Getenv("ANTHROPIC_API_KEY")
	return key != ""
}

func (p *provider) Models() []string {
	return []string{"minimax-m2.7", "minimax-m2.5"}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type requestBody struct {
	Model     string    `json:"model"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens"`
	Messages  []message `json:"messages"`
}

type chunk struct {
	Type  string `json:"type"`
	Delta struct {
		Text string `json:"text"`
	} `json:"delta"`
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY not set")
	}

	body := requestBody{
		Model:     model,
		Stream:    true,
		MaxTokens: 4096,
		Messages:  []message{},
	}
	if system != "" {
		body.Messages = append(body.Messages, message{Role: "user", Content: prompt})
	} else {
		body.Messages = append(body.Messages, message{Role: "user", Content: prompt})
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")

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

		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			continue
		}
		if c.Type == "content_block_delta" {
			if c.Delta.Text != "" {
				handler.Chunk(c.Delta.Text)
			}
		}
	}

	handler.Summary(providers.UsageInfo{})
	handler.End()
	return nil
}
