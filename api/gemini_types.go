//go:build !nogemini

package api

import "encoding/json"

// GeminiContent and GeminiPart are used by providers/convert.go even when
// gemini support is excluded at build time (nogemini tag).
type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

// GeminiPart represents a single part within Gemini content.
// Only one of Text, FunctionCall, or FunctionResponse should be set.
type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
}

// GeminiFunctionCall represents a function call requested by the model.
type GeminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// GeminiFunctionResponse represents the result of a function call.
type GeminiFunctionResponse struct {
	Name     string `json:"name"`
	Response struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"response"`
}

// GeminiToolDeclaration describes a function that the model may call.
type GeminiToolDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// GeminiTools wraps the list of function declarations sent to the API.
type GeminiTools struct {
	FunctionDeclarations []GeminiToolDeclaration `json:"functionDeclarations"`
}

type GeminiRequest struct {
	Contents          []GeminiContent `json:"contents"`
	Stream            bool            `json:"stream"`
	SystemInstruction *struct {
		Parts []GeminiPart `json:"parts"`
	} `json:"systemInstruction,omitempty"`
	Tools []GeminiTools `json:"tools,omitempty"`
	// GenerationConfig carries sampling parameters. Gemini, unlike Anthropic
	// and the OpenAI chat-completions protocol, does not accept temperature
	// at the top level — it must sit inside this nested object. The pointer
	// is nil (not merely empty-struct) whenever no generation-config field
	// is set, so omitempty drops the whole "generationConfig" key rather
	// than sending "generationConfig":{}.
	GenerationConfig *GeminiGenerationConfig `json:"generationConfig,omitempty"`
}

// GeminiGenerationConfig holds sampling parameters nested under
// GeminiRequest.GenerationConfig. Temperature is a pointer for the same
// reason as AnthropicRequest.Temperature and ChatRequest.Temperature: 0 is
// a meaningful, explicit value distinct from "unset".
type GeminiGenerationConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
	// MaxOutputTokens is Gemini's name for max_tokens. Omitted when zero,
	// leaving the provider's default in charge.
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}
