//go:build !noanthropic

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/stream"
)

// anthropicMessageUsage captures the usage inside a message_start event.
type anthropicMessageUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// anthropicMessage captures the message object inside message_start events.
type anthropicMessage struct {
	Usage *anthropicMessageUsage `json:"usage,omitempty"`
}

// anthropicContentBlock is used internally for parsing SSE content_block_start events.
// It is intentionally separate from the public AnthropicContentBlock (in anthropic_types.go)
// which is used for building outgoing requests. The SSE parser needs raw JSON fields
// (e.g. Input any) while the request builder needs structured tool result fields.
type anthropicContentBlock struct {
	Type string `json:"type"`           // "text" or "tool_use"
	Text string `json:"text,omitempty"` // present for text blocks
	ID   string `json:"id,omitempty"`   // present for tool_use blocks
	Name string `json:"name,omitempty"` // present for tool_use blocks
	// Input may be an empty object (or partial) on content_block_start for tool_use.
	// Full arguments arrive via content_block_delta / input_json_delta.
	Input any `json:"input,omitempty"`
}

// anthropicDelta is the delta inside content_block_delta events.
type anthropicDelta struct {
	Type        string `json:"type"`                   // "text_delta" or "input_json_delta"
	Text        string `json:"text,omitempty"`         // present for text_delta
	PartialJSON string `json:"partial_json,omitempty"` // present for input_json_delta
}

// anthropicMessageDeltaUsage captures usage inside message_delta events.
type anthropicMessageDeltaUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// anthropicMessageDeltaStop captures the delta fields inside message_delta events.
type anthropicMessageDeltaStop struct {
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence"`
}

type anthropicStreamChunk struct {
	Type string `json:"type"`
	// Present in message_start events.
	Message *anthropicMessage `json:"message,omitempty"`
	// Present in content_block_start events.
	Index        int                    `json:"index,omitempty"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
	// Raw delta — shape depends on event type:
	//   content_block_delta → anthropicDelta (Type, Text, PartialJSON)
	//   message_delta       → anthropicMessageDeltaStop (StopReason, StopSequence)
	Delta json.RawMessage `json:"delta,omitempty"`
	// Present in message_delta events.
	Usage *anthropicMessageDeltaUsage `json:"usage,omitempty"`
}

// toolAccumulator tracks a single tool call being accumulated from stream events.
type toolAccumulator struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

// AnthropicStreamer streams a request against the Anthropic Messages
// protocol. The zero value is usable and behaves exactly like the old
// package-level StreamAnthropic function.
type AnthropicStreamer struct {
	// HTTP is the client to send with. nil means "resolve from the context"
	// (see doer); it is the seam the connector layer injects through.
	HTTP HTTPDoer
	// Headers are extra request headers, applied after the protocol defaults.
	Headers map[string]string
}

// Stream POSTs body to endpoint and calls emit for every decoded SSE event.
func (s AnthropicStreamer) Stream(ctx context.Context, apiKey, endpoint string, body AnthropicRequest, emit func(stream.Event) error) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	dl := debug.FromContext(ctx)
	if dl != nil {
		dl.WriteRequest("POST", endpoint, jsonBody)
	}

	req, err := http.NewRequestWithContext(withPhaseTrace(ctx, emit), "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", "2023-06-01")
	applyExtraHeaders(req, s.Headers)

	resp, err := doer(s.HTTP).Do(req)
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
	var inputTokens, outputTokens, cacheRead, cacheWrite int
	var finishReason string
	// toolAcc maps content_block index → tool accumulator
	toolAcc := make(map[int]*toolAccumulator)
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

		processChunk := func(chunk anthropicStreamChunk) error {
			switch chunk.Type {
			case "message_start":
				if chunk.Message != nil && chunk.Message.Usage != nil {
					inputTokens = chunk.Message.Usage.InputTokens
					cacheRead = chunk.Message.Usage.CacheReadInputTokens
					cacheWrite = chunk.Message.Usage.CacheCreationInputTokens
				}

			case "content_block_start":
				if chunk.ContentBlock == nil {
					return nil
				}
				cb := chunk.ContentBlock
				switch cb.Type {
				case "text":
					if cb.Text != "" {
						if err := emit(stream.TextDelta{Text: cb.Text}); err != nil {
							return err
						}
					}
				case "tool_use":
					acc := &toolAccumulator{
						ID:   cb.ID,
						Name: cb.Name,
					}
					// The initial input may contain partial arguments.
					if cb.Input != nil {
						if argsJSON, err := json.Marshal(cb.Input); err == nil && len(argsJSON) > 2 {
							// Only if it's not an empty object "{}"
							acc.Arguments.Write(argsJSON)
						}
					}
					toolAcc[chunk.Index] = acc
					if err := emit(stream.ToolCallStart{ID: acc.ID, Name: acc.Name}); err != nil {
						return err
					}
					// Emit any initial arguments as delta
					if acc.Arguments.Len() > 0 {
						if err := emit(stream.ToolCallDelta{ID: acc.ID, Delta: acc.Arguments.String()}); err != nil {
							return err
						}
					}
				}

			case "content_block_delta":
				if len(chunk.Delta) == 0 {
					return nil
				}
				var d anthropicDelta
				if err := json.Unmarshal(chunk.Delta, &d); err != nil {
					return fmt.Errorf("failed to parse content_block_delta: %w", err)
				}
				switch d.Type {
				case "text_delta":
					if d.Text != "" {
						if err := emit(stream.TextDelta{Text: d.Text}); err != nil {
							return err
						}
					}
				case "input_json_delta":
					if d.PartialJSON == "" {
						return nil
					}
					// Find the tool accumulator for this index
					if acc, ok := toolAcc[chunk.Index]; ok {
						acc.Arguments.WriteString(d.PartialJSON)
						if err := emit(stream.ToolCallDelta{ID: acc.ID, Delta: d.PartialJSON}); err != nil {
							return err
						}
					}
				}

			case "content_block_stop":
				// Finalize tool call if we have one at this index. Skip
				// malformed tool_use blocks: an empty name yields
				// "tool_calls[0] is missing a function name" and an empty ID
				// later yields "missing field `tool_call_id`" on the matching
				// tool-result message. Generate a stable fallback ID when the
				// provider returned none.
				if acc, ok := toolAcc[chunk.Index]; ok {
					if acc.Name != "" {
						id := acc.ID
						if id == "" {
							id = fmt.Sprintf("toolu_%d", chunk.Index)
						}
						if err := emit(stream.ToolCall{
							ID:        id,
							Name:      acc.Name,
							Arguments: acc.Arguments.String(),
						}); err != nil {
							return err
						}
					}
					delete(toolAcc, chunk.Index)
				}

			case "message_delta":
				if chunk.Usage != nil {
					outputTokens = chunk.Usage.OutputTokens
				}
				// Extract stop_reason from message_delta.delta
				if len(chunk.Delta) > 0 {
					var md anthropicMessageDeltaStop
					if err := json.Unmarshal(chunk.Delta, &md); err == nil && md.StopReason != "" {
						finishReason = md.StopReason
					}
				}

			case "message_stop":
				// No data to extract; the subsequent [DONE] line will end the loop.
			}
			return nil
		}

		if strings.HasPrefix(data, "[") {
			var chunks []anthropicStreamChunk
			if err := json.Unmarshal([]byte(data), &chunks); err != nil {
				if readErr != nil {
					break
				}
				continue
			}
			for _, chunk := range chunks {
				if err := processChunk(chunk); err != nil {
					return err
				}
			}
			if readErr != nil {
				break
			}
			continue
		}

		var chunk anthropicStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if readErr != nil {
				break
			}
			continue
		}
		if err := processChunk(chunk); err != nil {
			return err
		}

		if readErr != nil {
			break
		}
	}

	// Emit any remaining tool calls that were never stopped. Skip
	// malformed entries (no name); generate a stable fallback ID when
	// the provider returned none so the tool result can be paired back.
	for i, acc := range toolAcc {
		if acc.Name == "" {
			continue
		}
		id := acc.ID
		if id == "" {
			id = fmt.Sprintf("toolu_%d", i)
		}
		if err := emit(stream.ToolCall{
			ID:        id,
			Name:      acc.Name,
			Arguments: acc.Arguments.String(),
		}); err != nil {
			return err
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:      inputTokens,
			Output:     outputTokens,
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

// ConvertToolsToAnthropic converts tool schemas from OpenAI format to Anthropic format.
// OpenAI format:  [{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}]
// Anthropic format: [{"name":"...","description":"...","input_schema":{...}}]
func ConvertToolsToAnthropic(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 || string(tools) == "null" || string(tools) == "[]" {
		return tools
	}

	var openaiTools []map[string]any
	if err := json.Unmarshal(tools, &openaiTools); err != nil {
		log.Printf("warning: ConvertToolsToAnthropic failed to parse tools: %v — returning as-is", err)
		return tools
	}

	anthropicTools := make([]map[string]any, 0, len(openaiTools))
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
		inputSchema, _ := fn["parameters"].(map[string]any)

		at := map[string]any{
			"name": name,
		}
		if desc != "" {
			at["description"] = desc
		}
		if inputSchema != nil {
			at["input_schema"] = inputSchema
		}
		anthropicTools = append(anthropicTools, at)
	}

	if len(anthropicTools) == 0 {
		return tools
	}

	result, err := json.Marshal(anthropicTools)
	if err != nil {
		return tools
	}
	return result
}

// MarkLastToolCached appends cache_control to the last entry of an
// already-converted Anthropic tools array, so the whole schema block becomes a
// cacheable prefix.
//
// It works on the marshalled JSON rather than on the tool structs because the
// schemas arrive here as an opaque json.RawMessage built elsewhere; parsing
// into []map[string]any and back is the same round trip ConvertToolsToAnthropic
// already does, and it keeps the tool type free of transport concerns.
//
// A tools array that cannot be parsed is returned untouched: losing the cache
// is a cost, losing the tools is a broken request.
func MarkLastToolCached(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 || string(tools) == "null" || string(tools) == "[]" {
		return tools
	}
	var list []map[string]any
	if err := json.Unmarshal(tools, &list); err != nil || len(list) == 0 {
		return tools
	}
	list[len(list)-1]["cache_control"] = CacheEphemeral()
	out, err := json.Marshal(list)
	if err != nil {
		return tools
	}
	return out
}

// MarkLastSystemBlockCached marks the end of the system prompt as a cacheable
// prefix. A nil or empty system prompt is left alone — there is nothing to
// cache, and an empty block would be rejected.
func MarkLastSystemBlockCached(system []AnthropicSystemBlock) []AnthropicSystemBlock {
	if len(system) == 0 {
		return system
	}
	system[len(system)-1].CacheControl = CacheEphemeral()
	return system
}
