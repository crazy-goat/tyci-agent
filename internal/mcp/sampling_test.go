package mcp

import (
	"encoding/json"
	"testing"
)

func TestSamplingCreateMessageRequest(t *testing.T) {
	req := SamplingCreateMessageRequest(1, &SamplingRequest{
		Messages: []SamplingMessage{
			{Role: "user", Content: json.RawMessage(`{"type":"text","text":"hello"}`)},
		},
		MaxTokens: 100,
	})

	if req.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want \"2.0\"", req.JSONRPC)
	}
	if req.Method != "sampling/createMessage" {
		t.Errorf("Method = %q, want \"sampling/createMessage\"", req.Method)
	}
	if req.ID != 1 {
		t.Errorf("ID = %d, want 1", req.ID)
	}
}

func TestElicitationCreateRequest(t *testing.T) {
	req := ElicitationCreateRequest(2, &ElicitationRequest{
		Message: "What is your name?",
	})

	if req.JSONRPC != "2.0" {
		t.Errorf("JSONRPC = %q, want \"2.0\"", req.JSONRPC)
	}
	if req.Method != "elicitation/create" {
		t.Errorf("Method = %q, want \"elicitation/create\"", req.Method)
	}
	if req.ID != 2 {
		t.Errorf("ID = %d, want 2", req.ID)
	}
}

func TestSamplingRequestJSON(t *testing.T) {
	req := &SamplingRequest{
		Messages: []SamplingMessage{
			{Role: "user", Content: json.RawMessage(`{"type":"text","text":"hello"}`)},
		},
		MaxTokens: 100,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded SamplingRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if len(decoded.Messages) != 1 {
		t.Errorf("Messages len = %d, want 1", len(decoded.Messages))
	}
	if decoded.Messages[0].Role != "user" {
		t.Errorf("Role = %q, want \"user\"", decoded.Messages[0].Role)
	}
}

func TestElicitationRequestJSON(t *testing.T) {
	req := &ElicitationRequest{
		Message: "What is your name?",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded ElicitationRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.Message != "What is your name?" {
		t.Errorf("Message = %q, want \"What is your name?\"", decoded.Message)
	}
}
