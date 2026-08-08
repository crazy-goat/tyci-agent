package connector

import (
	"context"
	"encoding/json"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// gemini speaks the Gemini generateContent protocol.
//
// TODO(etap-3, docs/architecture-refactor.md "Bugi znalezione przy etapie 0"):
// this connector should build its own URL
// (/v1beta/models/<model>:streamGenerateContent) and send x-goog-api-key
// instead of Authorization: Bearer. Both are deliberately left broken here —
// the golden files freeze today's behaviour and the fix needs a real API key
// to verify.
type gemini struct {
	ep Endpoint
}

// NewGemini builds a Gemini generateContent connector.
func NewGemini(ep Endpoint) (Connector, error) {
	return &gemini{ep: ep}, nil
}

func (c *gemini) Kind() string { return KindGemini }

func (c *gemini) Stream(ctx context.Context, req Request, emit func(stream.Event) error) error {
	contents, system := messagesToGemini(req.Messages)
	body := api.GeminiRequest{
		Contents: contents,
		Stream:   true,
	}
	if system != "" {
		body.SystemInstruction = &struct {
			Parts []api.GeminiPart `json:"parts"`
		}{Parts: []api.GeminiPart{{Text: system}}}
	} else if req.System != "" {
		body.SystemInstruction = &struct {
			Parts []api.GeminiPart `json:"parts"`
		}{Parts: []api.GeminiPart{{Text: req.System}}}
	}
	// Convert tools from OpenAI format to Gemini functionDeclarations
	if len(req.Tools) > 0 && string(req.Tools) != "null" && string(req.Tools) != "[]" {
		body.Tools = toolsToGemini(req.Tools)
	}
	return api.StreamGemini(ctx, c.ep.APIKey, c.ep.URL(), body, emit)
}

// messagesToGemini converts a Message slice to Gemini content and system
// instruction. Returns contents and any system instruction text extracted
// from the messages.
func messagesToGemini(msgs []Message) ([]api.GeminiContent, string) {
	var contents []api.GeminiContent
	var systemText string
	for _, m := range msgs {
		role := m.Role
		// Map "toolResult" → "function" for Gemini (Gemini uses "function" role for tool results)
		if role == "toolResult" {
			role = "function"
		}
		// Map "system" role to system instruction
		if role == "system" {
			for _, block := range m.Content {
				if block.Type == "text" {
					systemText += block.Text
				}
			}
			continue
		}

		var parts []api.GeminiPart
		for _, block := range m.Content {
			switch block.Type {
			case "text":
				// For tool results with a ToolCallID, wrap as functionResponse
				if role == "function" && block.ToolCallID != "" {
					parts = append(parts, api.GeminiPart{
						FunctionResponse: &api.GeminiFunctionResponse{
							Name: block.ToolName,
							Response: struct {
								Name    string `json:"name"`
								Content string `json:"content"`
							}{
								Name:    block.ToolName,
								Content: block.Text,
							},
						},
					})
				} else {
					parts = append(parts, api.GeminiPart{Text: block.Text})
				}
			case "thinking":
				// Skip thinking blocks for Gemini
			case "toolCall":
				// Convert tool calls to functionCall parts for Gemini
				var args json.RawMessage
				if block.Arguments != nil {
					args = block.Arguments
				} else {
					args = json.RawMessage("{}")
				}
				parts = append(parts, api.GeminiPart{
					FunctionCall: &api.GeminiFunctionCall{
						Name: block.Name,
						Args: args,
					},
				})
			case "toolResult":
				// Convert tool results to functionResponse parts for Gemini
				parts = append(parts, api.GeminiPart{
					FunctionResponse: &api.GeminiFunctionResponse{
						Name: block.ToolName,
						Response: struct {
							Name    string `json:"name"`
							Content string `json:"content"`
						}{
							Name:    block.ToolName,
							Content: block.Text,
						},
					},
				})
			}
		}

		if len(parts) > 0 || role == "function" {
			contents = append(contents, api.GeminiContent{Parts: parts, Role: role})
		}
	}
	return contents, systemText
}

// toolsToGemini converts tool schemas from OpenAI format to Gemini functionDeclarations format.
// OpenAI format:  [{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}]
// Gemini format: [{"functionDeclarations":[{"name":"...","description":"...","parameters":{...}}]}]
func toolsToGemini(tools json.RawMessage) []api.GeminiTools {
	var openaiTools []map[string]any
	if err := json.Unmarshal(tools, &openaiTools); err != nil {
		return nil
	}

	declarations := make([]api.GeminiToolDeclaration, 0, len(openaiTools))
	for _, t := range openaiTools {
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)

		var params json.RawMessage
		if p, ok := fn["parameters"]; ok {
			if data, err := json.Marshal(p); err == nil {
				params = data
			}
		}

		declarations = append(declarations, api.GeminiToolDeclaration{
			Name:        name,
			Description: desc,
			Parameters:  params,
		})
	}

	if len(declarations) == 0 {
		return nil
	}

	return []api.GeminiTools{{FunctionDeclarations: declarations}}
}
