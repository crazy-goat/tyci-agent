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

	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/stream"
)

// ResponsesContentPart is one text part in a Responses API message input.
type ResponsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ResponsesInputItem is an item in the Responses API input array. Message,
// function_call, and function_call_output use different subsets of these
// fields; pointers keep function-only fields out of message items while still
// allowing an empty arguments/output string to be sent explicitly.
type ResponsesInputItem struct {
	Type      string                 `json:"type,omitempty"`
	Role      string                 `json:"role,omitempty"`
	Content   []ResponsesContentPart `json:"content,omitempty"`
	ID        string                 `json:"id,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments *string                `json:"arguments,omitempty"`
	Output    *string                `json:"output,omitempty"`
	Status    string                 `json:"status,omitempty"`
}

// ResponsesReasoning configures the model's reasoning effort.
type ResponsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// ResponsesRequest is the wire request for the OpenAI Responses API.
type ResponsesRequest struct {
	Model           string               `json:"model"`
	Instructions    string               `json:"instructions,omitempty"`
	Input           []ResponsesInputItem `json:"input"`
	Tools           json.RawMessage      `json:"tools,omitempty"`
	Stream          bool                 `json:"stream"`
	Temperature     *float64             `json:"temperature,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Reasoning       *ResponsesReasoning  `json:"reasoning,omitempty"`
}

type responsesOutputContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesOutputItem struct {
	Type      string                   `json:"type"`
	ID        string                   `json:"id,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
	Status    string                   `json:"status,omitempty"`
	Content   []responsesOutputContent `json:"content,omitempty"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	InputDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
	OutputDetails *struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details,omitempty"`
}

type responsesIncompleteDetails struct {
	Reason string `json:"reason,omitempty"`
}

type responsesError struct {
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
}

type responsesAPIResponse struct {
	Status            string                      `json:"status,omitempty"`
	Usage             *responsesUsage             `json:"usage,omitempty"`
	IncompleteDetails *responsesIncompleteDetails `json:"incomplete_details,omitempty"`
	Error             *responsesError             `json:"error,omitempty"`
}

type responsesStreamEvent struct {
	Type      string                `json:"type"`
	Delta     string                `json:"delta,omitempty"`
	Text      string                `json:"text,omitempty"`
	Item      *responsesOutputItem  `json:"item,omitempty"`
	Response  *responsesAPIResponse `json:"response,omitempty"`
	Error     *responsesError       `json:"error,omitempty"`
	ItemID    string                `json:"item_id,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
}

type responsesToolAccumulator struct {
	ItemID    string
	CallID    string
	Name      string
	Arguments strings.Builder
	Fallback  string
	Started   bool
	Emitted   bool
}

// ResponsesStreamer streams a request against the OpenAI Responses protocol.
type ResponsesStreamer struct {
	// HTTP is the client to send with. nil means the shared default client.
	HTTP HTTPDoer
	// Headers are extra request headers, applied after protocol defaults.
	Headers map[string]string
}

// Stream POSTs body to endpoint and calls emit for every decoded SSE event.
func (s ResponsesStreamer) Stream(ctx context.Context, apiKey, endpoint string, body ResponsesRequest, emit func(stream.Event) error) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	dl := debug.FromContext(ctx)
	if dl != nil {
		dl.WriteRequest("POST", endpoint, jsonBody)
	}

	traceCtx, stopTrace := withPhaseTrace(ctx, emit)
	defer stopTrace()

	req, err := http.NewRequestWithContext(traceCtx, "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	applyExtraHeaders(req, s.Headers)

	resp, err := doer(s.HTTP).Do(req)
	// The httptrace hooks race Do() returning (net/http gives no
	// ordering guarantee between them), so stop the trace right here,
	// before touching resp or err, to guarantee every phase emit has
	// already happened — see phaseTrace.stop's doc comment.
	stopTrace()
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		bodyStr := string(bodyBytes)
		if dl != nil {
			dl.WriteResponse(resp.StatusCode, bodyBytes)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			return &RetryableError{Code: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After"), Message: fmt.Sprintf("429 rate limited: %s", bodyStr)}
		}
		if resp.StatusCode >= 500 {
			return &RetryableError{Code: resp.StatusCode, Message: fmt.Sprintf("%d server error: %s", resp.StatusCode, bodyStr)}
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, bodyStr)
	}

	reader := bufio.NewReader(resp.Body)
	toolsByAlias := make(map[string]*responsesToolAccumulator)
	var toolOrder []*responsesToolAccumulator
	var textEmitted bool
	var inputTokens, outputTokens, reasoningTokens, cacheRead int
	finishReason := ""
	var readErr error
	var eventName string

	getTool := func(itemID, callID string) *responsesToolAccumulator {
		if itemID != "" {
			if acc := toolsByAlias[itemID]; acc != nil {
				if callID != "" {
					toolsByAlias[callID] = acc
				}
				return acc
			}
		}
		if callID != "" {
			if acc := toolsByAlias[callID]; acc != nil {
				if itemID != "" {
					toolsByAlias[itemID] = acc
				}
				return acc
			}
		}

		acc := &responsesToolAccumulator{
			ItemID:   itemID,
			CallID:   callID,
			Fallback: fmt.Sprintf("call_%d", len(toolOrder)),
		}
		toolOrder = append(toolOrder, acc)
		if itemID != "" {
			toolsByAlias[itemID] = acc
		}
		if callID != "" {
			toolsByAlias[callID] = acc
		}
		return acc
	}

	toolID := func(acc *responsesToolAccumulator) string {
		if acc.CallID != "" {
			return acc.CallID
		}
		if acc.ItemID != "" {
			return acc.ItemID
		}
		return acc.Fallback
	}

	startTool := func(acc *responsesToolAccumulator) error {
		if acc.Started || acc.Name == "" {
			return nil
		}
		acc.Started = true
		return emit(stream.ToolCallStart{ID: toolID(acc), Name: acc.Name})
	}

	emitTool := func(acc *responsesToolAccumulator) error {
		if acc.Emitted || acc.Name == "" {
			return nil
		}
		if err := startTool(acc); err != nil {
			return err
		}
		acc.Emitted = true
		return emit(stream.ToolCall{ID: toolID(acc), Name: acc.Name, Arguments: acc.Arguments.String()})
	}

	appendToolArguments := func(acc *responsesToolAccumulator, delta string) error {
		if delta == "" {
			return nil
		}
		if err := startTool(acc); err != nil {
			return err
		}
		if err := emit(stream.ToolCallDelta{ID: toolID(acc), Delta: delta}); err != nil {
			return err
		}
		acc.Arguments.WriteString(delta)
		return nil
	}

	applyUsage := func(r *responsesAPIResponse) {
		if r == nil || r.Usage == nil {
			return
		}
		inputTokens = r.Usage.InputTokens
		outputTokens = r.Usage.OutputTokens
		if r.Usage.InputDetails != nil {
			cacheRead = r.Usage.InputDetails.CachedTokens
		}
		if r.Usage.OutputDetails != nil {
			reasoningTokens = r.Usage.OutputDetails.ReasoningTokens
		}
	}

	processEvent := func(ev responsesStreamEvent) error {
		switch ev.Type {
		case "response.output_text.delta":
			if ev.Delta != "" {
				textEmitted = true
				return emit(stream.TextDelta{Text: ev.Delta})
			}
		case "response.output_text.done":
			// Most Responses implementations send output_text.delta events.
			// Use the final text only as a fallback for gateways that do not.
			if !textEmitted && ev.Text != "" {
				textEmitted = true
				return emit(stream.TextDelta{Text: ev.Text})
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				return emit(stream.ThinkingDelta{Text: ev.Delta})
			}
		case "response.function_call_arguments.delta":
			acc := getTool(ev.ItemID, ev.CallID)
			if ev.CallID != "" {
				acc.CallID = ev.CallID
			}
			return appendToolArguments(acc, ev.Delta)
		case "response.function_call_arguments.done":
			acc := getTool(ev.ItemID, ev.CallID)
			if ev.CallID != "" {
				acc.CallID = ev.CallID
			}
			if ev.Arguments != "" && acc.Arguments.String() != ev.Arguments {
				acc.Arguments.Reset()
				acc.Arguments.WriteString(ev.Arguments)
			}
			return nil
		case "response.output_item.added", "response.output_item.done":
			if ev.Item == nil {
				return nil
			}
			if ev.Item.Type == "function_call" {
				acc := getTool(ev.Item.ID, ev.Item.CallID)
				if ev.Item.ID != "" {
					acc.ItemID = ev.Item.ID
				}
				if ev.Item.CallID != "" {
					acc.CallID = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					acc.Name = ev.Item.Name
				}
				if err := startTool(acc); err != nil {
					return err
				}
				if ev.Type == "response.output_item.added" {
					if acc.Arguments.Len() == 0 {
						if err := appendToolArguments(acc, ev.Item.Arguments); err != nil {
							return err
						}
					}
				} else {
					// output_item.done carries the complete argument string. Do
					// not emit it a second time when deltas already arrived.
					if ev.Item.Arguments != "" {
						if acc.Arguments.Len() == 0 {
							if err := appendToolArguments(acc, ev.Item.Arguments); err != nil {
								return err
							}
						} else if acc.Arguments.String() != ev.Item.Arguments {
							acc.Arguments.Reset()
							acc.Arguments.WriteString(ev.Item.Arguments)
						}
					}
					return emitTool(acc)
				}
				return nil
			}
			if ev.Type == "response.output_item.done" && !textEmitted && ev.Item.Type == "message" {
				for _, part := range ev.Item.Content {
					if part.Type == "output_text" && part.Text != "" {
						textEmitted = true
						if err := emit(stream.TextDelta{Text: part.Text}); err != nil {
							return err
						}
					}
				}
			}
		case "response.completed", "response.done":
			applyUsage(ev.Response)
			if ev.Response != nil {
				switch ev.Response.Status {
				case "completed":
					finishReason = "stop"
				case "incomplete":
					finishReason = "length"
				case "failed":
					finishReason = "error"
				}
			}
		case "response.incomplete":
			applyUsage(ev.Response)
			finishReason = "length"
			if ev.Response != nil && ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason != "max_output_tokens" {
				finishReason = ev.Response.IncompleteDetails.Reason
			}
		case "response.failed", "error":
			message := "Responses API request failed"
			if ev.Error != nil && ev.Error.Message != "" {
				message = ev.Error.Message
			} else if ev.Response != nil && ev.Response.Error != nil && ev.Response.Error.Message != "" {
				message = ev.Response.Error.Message
			}
			return errors.New(message)
		}
		return nil
	}

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
		if line == "" {
			eventName = ""
			if readErr != nil {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			if readErr != nil {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			if readErr != nil {
				break
			}
			continue
		}

		data := strings.TrimLeft(strings.TrimPrefix(line, "data:"), " \t")
		if data == "[DONE]" {
			break
		}

		var ev responsesStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			if readErr != nil {
				break
			}
			continue
		}
		if ev.Type == "" {
			ev.Type = eventName
		}
		eventName = ""
		if err := processEvent(ev); err != nil {
			return err
		}

		if readErr != nil {
			break
		}
	}

	for _, acc := range toolOrder {
		if err := emitTool(acc); err != nil {
			return err
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := emit(stream.Finish{
		Reason: finishReason,
		Usage: stream.Usage{
			Input:     inputTokens,
			Output:    outputTokens,
			Reasoning: reasoningTokens,
			CacheRead: cacheRead,
		},
	}); err != nil {
		return err
	}

	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	return nil
}
