//go:build !noanthropic && !nogemini

package providers

// Characterization ("golden") tests that freeze the wire format produced by
// dynamicProvider.Stream for every supported apiType.
//
// These tests are a safety net for the connector refactor (docs/architecture-refactor.md,
// Etap 0). They deliberately assert on TODAY's behaviour — including its quirks —
// so that any silent change in message conversion, endpoint resolution, headers or
// stream-event mapping shows up as a diff.
//
// Hermetic by construction:
//   - the provider is built by hand, so the global `providers` registry is untouched,
//   - the API token is embedded literally in the URI, so neither ~/.tyci/auth.json
//     nor *_API_KEY environment variables are ever consulted,
//   - parseURI hard-codes "https://"+host, so the fake server must speak TLS;
//     the insecure client is injected through api.HTTPClientKey.
//
// Regenerate with:
//
//	go test ./providers/ -run TestWireGolden -update

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/decodo/tyci/api"
	"github.com/decodo/tyci/stream"
)

var update = flag.Bool("update", false, "regenerate providers/testdata golden files")

// capturedHeaders is the whitelist of request headers frozen in the goldens.
// Everything else (Content-Length, User-Agent, Host, ...) is environment noise.
var capturedHeaders = []string{
	"Accept",
	"Anthropic-Version",
	"Authorization",
	"Content-Type",
}

// requestRecord is the shape stored in wire_<apitype>_request.golden.json.
type requestRecord struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

// usageRecord mirrors stream.Usage with stable JSON keys.
type usageRecord struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Reasoning  int `json:"reasoning"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
}

// eventRecord is the shape stored in wire_<apitype>_events.golden.json.
type eventRecord struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	ID        string       `json:"id,omitempty"`
	Name      string       `json:"name,omitempty"`
	Delta     string       `json:"delta,omitempty"`
	Arguments string       `json:"arguments,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Usage     *usageRecord `json:"usage,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// dirtyRequest builds one deliberately messy Request reused by all three
// apiTypes. It exercises exactly the block kinds whose handling diverges
// between providers and which are the easiest to lose in a refactor:
// a system prompt, plain text, a thinking block, a tool call, and two tool
// results (one plain, one flagged IsError).
func dirtyRequest(model string) Request {
	return Request{
		Model:  model,
		System: "You are a terse test agent. Answer with tools when possible.",
		Messages: []RichMessage{
			{
				Role: "user",
				Content: []ContentBlock{
					{Type: "text", Text: "What is the weather in London?"},
				},
			},
			{
				Role: "assistant",
				Content: []ContentBlock{
					{Type: "thinking", Thinking: "The user asked for weather. I should call get_weather."},
					{
						Type:      "toolCall",
						ID:        "call_1",
						Name:      "get_weather",
						Arguments: json.RawMessage(`{"location":"London"}`),
					},
				},
			},
			{
				Role: "toolResult",
				Content: []ContentBlock{
					{
						Type:       "toolResult",
						Text:       "18C, cloudy",
						ToolCallID: "call_1",
						ToolName:   "get_weather",
					},
					{
						Type:       "toolResult",
						Text:       "upstream weather service exploded",
						ToolCallID: "call_2",
						ToolName:   "get_weather",
						IsError:    true,
					},
				},
			},
		},
		Tools: json.RawMessage(`[{"type":"function","function":{"name":"get_weather","description":"Get the current weather for a location.","parameters":{"type":"object","properties":{"location":{"type":"string","description":"City name"}},"required":["location"]}}}]`),
	}
}

// sseFixtures holds the canned SSE responses per apiType. They are adapted
// from the existing unit tests in api/api_test.go and api/anthropic_test.go so
// each stream covers text, a tool call and usage.
var sseFixtures = map[string]string{
	"openai": `data: {"choices":[{"delta":{"reasoning_content":"let me check the tool"}}]}
data: {"choices":[{"delta":{"content":"Hello"}}]}
data: {"choices":[{"delta":{"content":" world"}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"loc"}}]}}]}
data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NY\"}"}}]}}]}
data: {"choices":[{"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}
data: [DONE]
`,
	"anthropic": `data: {"type":"message_start","message":{"usage":{"input_tokens":10}}}
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Hello"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}
data: {"type":"content_block_stop","index":0}
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"get_weather","input":{}}}
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"location\":"}}
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"London\"}"}}
data: {"type":"content_block_stop","index":1}
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}
data: {"type":"message_stop"}
data: [DONE]
`,
	"gemini": `data: {"candidates":[{"content":{"parts":[{"text":"Hello"},{"functionCall":{"name":"get_weather","args":{"location":"NY"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}
data: [DONE]
`,
}

func TestWireGolden(t *testing.T) {
	cases := []struct {
		apiType string
		model   string
		token   string
	}{
		{apiType: "openai", model: "wire-openai-model", token: "tok-openai"},
		{apiType: "anthropic", model: "wire-anthropic-model", token: "tok-anthropic"},
		{apiType: "gemini", model: "wire-gemini-model", token: "tok-gemini"},
	}

	for _, tc := range cases {
		t.Run(tc.apiType, func(t *testing.T) {
			t.Parallel()

			var (
				gotRecord requestRecord
				sawCall   bool
			)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawCall = true
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				gotRecord = requestRecord{
					Method:  r.Method,
					Path:    r.URL.RequestURI(),
					Headers: pickHeaders(r.Header),
					Body:    normalizeBody(raw),
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, sseFixtures[tc.apiType])
			}))
			defer srv.Close()

			// parseURI builds "https://"+host unconditionally, so the fake
			// server must be TLS and the client must skip verification.
			client := srv.Client()
			client.Timeout = 10 * time.Second

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, api.HTTPClientKey{}, client)

			uri := fmt.Sprintf("%s://%s@%s@%s", tc.apiType, tc.model, tc.token, srv.Listener.Addr().String())
			p := &dynamicProvider{
				name:    "wiretest-" + tc.apiType,
				entries: []ModelEntry{{Name: tc.model, URI: uri}},
			}

			ch, err := p.Stream(ctx, dirtyRequest(tc.model))
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}

			var events []eventRecord
			for ev := range ch {
				events = append(events, recordEvent(ev))
			}

			if !sawCall {
				t.Fatal("test server was never called")
			}

			reqJSON, err := json.MarshalIndent(gotRecord, "", "  ")
			if err != nil {
				t.Fatalf("marshalling request record: %v", err)
			}
			assertGolden(t, goldenPath("wire_"+tc.apiType+"_request.golden.json"), reqJSON)

			evJSON, err := json.MarshalIndent(events, "", "  ")
			if err != nil {
				t.Fatalf("marshalling event records: %v", err)
			}
			assertGolden(t, goldenPath("wire_"+tc.apiType+"_events.golden.json"), evJSON)
		})
	}
}

func goldenPath(name string) string {
	return filepath.Join("testdata", name)
}

// pickHeaders extracts the whitelisted headers in a deterministic form.
func pickHeaders(h http.Header) map[string]string {
	out := make(map[string]string)
	names := append([]string(nil), capturedHeaders...)
	sort.Strings(names)
	for _, n := range names {
		if v := h.Get(n); v != "" {
			out[n] = v
		}
	}
	return out
}

// normalizeBody keeps the body as JSON when it parses, so goldens stay
// readable and key-order independent; otherwise it stores it as a JSON string.
func normalizeBody(raw []byte) json.RawMessage {
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		s, _ := json.Marshal(string(raw))
		return s
	}
	return json.RawMessage(raw)
}

func recordEvent(ev stream.Event) eventRecord {
	switch v := ev.(type) {
	case stream.ThinkingDelta:
		return eventRecord{Type: "ThinkingDelta", Text: v.Text}
	case stream.TextDelta:
		return eventRecord{Type: "TextDelta", Text: v.Text}
	case stream.ToolCallStart:
		return eventRecord{Type: "ToolCallStart", ID: v.ID, Name: v.Name}
	case stream.ToolCallDelta:
		return eventRecord{Type: "ToolCallDelta", ID: v.ID, Delta: v.Delta}
	case stream.ToolCall:
		return eventRecord{Type: "ToolCall", ID: v.ID, Name: v.Name, Arguments: v.Arguments}
	case stream.Finish:
		return eventRecord{
			Type:   "Finish",
			Reason: v.Reason,
			Usage: &usageRecord{
				Input:      v.Usage.Input,
				Output:     v.Usage.Output,
				Reasoning:  v.Usage.Reasoning,
				CacheRead:  v.Usage.CacheRead,
				CacheWrite: v.Usage.CacheWrite,
			},
		}
	case stream.Retry:
		return eventRecord{Type: "Retry", Reason: v.Reason}
	case stream.StreamError:
		msg := ""
		if v.Err != nil {
			msg = v.Err.Error()
		}
		return eventRecord{Type: "StreamError", Error: msg}
	default:
		return eventRecord{Type: fmt.Sprintf("%T", ev)}
	}
}

// assertGolden compares got against the golden file at path on a normalized
// JSON value (unmarshal → reflect.DeepEqual), so key ordering never breaks it.
// With -update the golden is rewritten instead.
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()

	pretty := func(b []byte) string {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return string(b)
		}
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return string(b)
		}
		return string(out)
	}

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("creating testdata dir: %v", err)
		}
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		t.Logf("updated golden %s", path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v\nrun: go test ./providers/ -run TestWireGolden -update", path, err)
	}

	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("unmarshalling produced JSON for %s: %v", path, err)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("unmarshalling golden %s: %v", path, err)
	}

	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Errorf("wire format changed: %s\n"+
			"If this change is intentional, review it carefully, then run:\n"+
			"  go test ./providers/ -run TestWireGolden -update\n\n"+
			"--- got ---\n%s\n\n--- want ---\n%s",
			path, pretty(got), pretty(want))
	}
}
