package api

// GeminiContent and GeminiPart are used by providers/convert.go even when
// gemini support is excluded at build time (nogemini tag).
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
