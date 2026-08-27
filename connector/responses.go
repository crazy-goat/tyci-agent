package connector

import (
	"context"
	"encoding/json"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

// responses speaks the OpenAI Responses API. It is selected per model with
// ?api=responses in the provider URI, so a provider can mix Chat Completions
// and Responses models.
type responses struct {
	ep Endpoint
}

func init() { registerBuiltin(KindResponses, NewResponses) }

// NewResponses builds an OpenAI Responses API connector.
func NewResponses(ep Endpoint) (Connector, error) {
	return &responses{ep: ep}, nil
}

func (c *responses) Kind() string { return KindResponses }

func (c *responses) Stream(ctx context.Context, req Request, emit func(stream.Event) error) error {
	body := api.ResponsesRequest{
		Model:           req.Model,
		Instructions:    req.System,
		Input:           messagesToResponses(req.Messages),
		Tools:           convertToolsToResponses(req.Tools),
		Stream:          true,
		Temperature:     req.Temperature,
		MaxOutputTokens: req.MaxTokens,
	}
	if effort := c.ep.option(OptReasoningEffort); effort != "" {
		body.Reasoning = &api.ResponsesReasoning{Effort: effort}
	}
	url := c.ep.URL()
	if fallbacks := c.ep.option(OptFallbacks); fallbacks != "" {
		url = c.ep.URLWithQuery(map[string]string{OptFallbacks: fallbacks})
	}
	s := api.ResponsesStreamer{HTTP: c.ep.HTTP, Headers: c.ep.Headers}
	return s.Stream(ctx, c.ep.APIKey, url, body, emit)
}

func stringPointer(s string) *string { return &s }

// messagesToResponses converts the canonical conversation into Responses API
// input items. System instructions are sent separately by Stream, assistant
// tool calls become function_call items, and tool results become
// function_call_output items.
func messagesToResponses(msgs []Message) []api.ResponsesInputItem {
	result := make([]api.ResponsesInputItem, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool" || m.Role == "toolResult" {
			for _, block := range m.Content {
				if block.ToolCallID == "" {
					continue
				}
				result = append(result, api.ResponsesInputItem{
					Type:   "function_call_output",
					CallID: block.ToolCallID,
					Output: stringPointer(block.Text),
				})
			}
			continue
		}

		partType := "input_text"
		if m.Role == "assistant" {
			partType = "output_text"
		}
		var parts []api.ResponsesContentPart
		var calls []api.ResponsesInputItem
		for _, block := range m.Content {
			switch block.Type {
			case "text":
				parts = append(parts, api.ResponsesContentPart{Type: partType, Text: block.Text})
			case "toolCall":
				if block.Name == "" {
					continue
				}
				args := ""
				if block.Arguments != nil {
					args = string(block.Arguments)
				}
				calls = append(calls, api.ResponsesInputItem{
					Type:      "function_call",
					CallID:    block.ID,
					Name:      block.Name,
					Arguments: stringPointer(args),
					Status:    "completed",
				})
			}
		}
		if len(parts) > 0 {
			result = append(result, api.ResponsesInputItem{
				Role:    m.Role,
				Content: parts,
			})
		}
		result = append(result, calls...)
	}
	return result
}

// convertToolsToResponses converts the canonical OpenAI function-tool shape:
//
//	[{"type":"function","function":{"name":"...","parameters":{...}}}]
//
// into the flat Responses API shape:
//
//	[{"type":"function","name":"...","parameters":{...}}]
func convertToolsToResponses(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 || string(tools) == "null" || string(tools) == "[]" {
		return tools
	}

	var openAITools []map[string]any
	if err := json.Unmarshal(tools, &openAITools); err != nil {
		return tools
	}

	responsesTools := make([]map[string]any, 0, len(openAITools))
	for _, tool := range openAITools {
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			// Already in Responses format, or an unsupported tool type. Do
			// not destroy an opaque tool definition we do not understand.
			return tools
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		converted := map[string]any{
			"type":       "function",
			"name":       name,
			"parameters": fn["parameters"],
		}
		if description, ok := fn["description"].(string); ok && description != "" {
			converted["description"] = description
		}
		if strict, ok := fn["strict"]; ok {
			converted["strict"] = strict
		}
		responsesTools = append(responsesTools, converted)
	}
	if len(responsesTools) == 0 {
		return tools
	}

	result, err := json.Marshal(responsesTools)
	if err != nil {
		return tools
	}
	return result
}
