package opencodezen

import (
	"testing"
)

func TestProviderName(t *testing.T) {
	p := &provider{}
	if p.Name() != "opencodezen" {
		t.Errorf("expected 'opencodezen', got '%s'", p.Name())
	}
}

func TestProviderIsConfigured(t *testing.T) {
	p := &provider{}
	t.Setenv("OPENCODE_ZEN_API_KEY", "")
	if p.IsConfigured() {
		t.Error("expected false when API key not set")
	}
	t.Setenv("OPENCODE_ZEN_API_KEY", "test-key")
	if !p.IsConfigured() {
		t.Error("expected true when API key is set")
	}
}

func TestProviderModels(t *testing.T) {
	p := &provider{}
	models := p.Models()

	expected := []string{
		"gpt-5.4", "gpt-5.4-pro", "gpt-5.4-mini", "gpt-5.4-nano",
		"gpt-5.3-codex", "gpt-5.3-codex-spark",
		"gpt-5.2", "gpt-5.2-codex",
		"gpt-5.1", "gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini",
		"gpt-5", "gpt-5-codex", "gpt-5-nano",
		"claude-opus-4-6", "claude-opus-4-5", "claude-opus-4-1",
		"claude-sonnet-4-6", "claude-sonnet-4-5", "claude-sonnet-4",
		"claude-haiku-4-5", "claude-3-5-haiku",
		"gemini-3.1-pro", "gemini-3-flash",
		"glm-5.1", "glm-5", "kimi-k2.5",
		"minimax-m2.5", "minimax-m2.5-free",
		"big-pickle",
		"mimo-v2-pro-free", "mimo-v2-omni-free",
		"qwen3.6-plus-free", "nemotron-3-super-free",
	}

	if len(models) != len(expected) {
		t.Errorf("expected %d models, got %d", len(expected), len(models))
	}

	modelMap := make(map[string]bool)
	for _, m := range models {
		modelMap[m] = true
	}
	for _, m := range expected {
		if !modelMap[m] {
			t.Errorf("missing model: %s", m)
		}
	}
}

func TestModelEndpoint(t *testing.T) {
	tests := []struct {
		model    string
		endpoint string
	}{
		{"gpt-5.4", "/responses"},
		{"gpt-5.4-pro", "/responses"},
		{"gpt-5.4-mini", "/responses"},
		{"gpt-5.4-nano", "/responses"},
		{"gpt-5.3-codex", "/responses"},
		{"gpt-5.3-codex-spark", "/responses"},
		{"gpt-5.2", "/responses"},
		{"gpt-5.2-codex", "/responses"},
		{"gpt-5.1", "/responses"},
		{"gpt-5.1-codex", "/responses"},
		{"gpt-5.1-codex-max", "/responses"},
		{"gpt-5.1-codex-mini", "/responses"},
		{"gpt-5", "/responses"},
		{"gpt-5-codex", "/responses"},
		{"gpt-5-nano", "/responses"},
		{"glm-5.1", "/chat/completions"},
		{"glm-5", "/chat/completions"},
		{"kimi-k2.5", "/chat/completions"},
		{"minimax-m2.5", "/chat/completions"},
		{"minimax-m2.5-free", "/chat/completions"},
		{"big-pickle", "/chat/completions"},
		{"mimo-v2-pro-free", "/chat/completions"},
		{"mimo-v2-omni-free", "/chat/completions"},
		{"qwen3.6-plus-free", "/chat/completions"},
		{"nemotron-3-super-free", "/chat/completions"},
		{"claude-opus-4-6", "/messages"},
		{"claude-opus-4-5", "/messages"},
		{"claude-opus-4-1", "/messages"},
		{"claude-sonnet-4-6", "/messages"},
		{"claude-sonnet-4-5", "/messages"},
		{"claude-sonnet-4", "/messages"},
		{"claude-haiku-4-5", "/messages"},
		{"claude-3-5-haiku", "/messages"},
		{"gemini-3.1-pro", "/models/gemini-3.1-pro"},
		{"gemini-3-flash", "/models/gemini-3-flash"},
	}

	for _, tt := range tests {
		result := modelEndpoint(tt.model)
		if result != baseURL+tt.endpoint {
			t.Errorf("modelEndpoint(%s) = %s, want %s", tt.model, result, baseURL+tt.endpoint)
		}
	}
}

func TestModelEndpointUnknown(t *testing.T) {
	result := modelEndpoint("unknown-model")
	if result != baseURL+"/chat/completions" {
		t.Errorf("modelEndpoint(unknown) = %s, want %s", result, baseURL+"/chat/completions")
	}
}
