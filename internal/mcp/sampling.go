package mcp

import (
	"context"
	"encoding/json"
)

// SamplingRequest represents a sampling/createMessage request from the server.
type SamplingRequest struct {
	Messages         []SamplingMessage `json:"messages"`
	ModelPreferences *ModelPreferences `json:"modelPreferences,omitempty"`
	SystemPrompt     string            `json:"systemPrompt,omitempty"`
	MaxTokens        int               `json:"maxTokens"`
	Temperature      *float64          `json:"temperature,omitempty"`
	StopSequences    []string          `json:"stopSequences,omitempty"`
}

// SamplingMessage represents a message in a sampling request.
type SamplingMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ModelPreferences indicates which models the server prefers.
type ModelPreferences struct {
	Hints []ModelHint `json:"hints,omitempty"`
}

// ModelHint provides a hint about which model to use.
type ModelHint struct {
	Name string `json:"name,omitempty"`
}

// SamplingResult represents the result of a sampling request.
type SamplingResult struct {
	Model      string          `json:"model"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stopReason,omitempty"`
}

// SamplingResultContent represents the content in a sampling result.
type SamplingResultContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ElicitationRequest represents an elicitation/create request from the server.
type ElicitationRequest struct {
	Message string          `json:"message"`
	Schema  json.RawMessage `json:"requestedSchema,omitempty"`
}

// ElicitationResult represents the result of an elicitation request.
type ElicitationResult struct {
	Action  string          `json:"action"` // "accept", "cancel", "decline"
	Content json.RawMessage `json:"content,omitempty"`
}

// SamplingHandler is called when an MCP server requests sampling.
type SamplingHandler func(ctx context.Context, serverName string, req *SamplingRequest) (*SamplingResult, error)

// ElicitationHandler is called when an MCP server requests user input.
type ElicitationHandler func(ctx context.Context, serverName string, req *ElicitationRequest) (*ElicitationResult, error)

// SamplingCreateMessageRequest creates a sampling/createMessage request.
func SamplingCreateMessageRequest(id int, params *SamplingRequest) Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "sampling/createMessage",
		Params:  params,
		ID:      id,
	}
}

// ElicitationCreateRequest creates an elicitation/create request.
func ElicitationCreateRequest(id int, params *ElicitationRequest) Request {
	return Request{
		JSONRPC: "2.0",
		Method:  "elicitation/create",
		Params:  params,
		ID:      id,
	}
}
