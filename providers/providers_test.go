package providers

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// =============================================================================
// LoadConfig tests
// =============================================================================

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string][]ModelEntry
		wantErr bool
	}{
		{
			name:    "file not found",
			content: "",
			want:    nil,
			wantErr: false,
		},
		{
			name: "new format",
			content: `{
				"openai": {
					"gpt-4": {"uri": "openai://gpt-4@sk-test@api.openai.com"},
					"gpt-3.5": {"uri": "openai://gpt-3.5-turbo@api.openai.com"}
				}
			}`,
			want: map[string][]ModelEntry{
				"openai": {
					{Name: "gpt-4", URI: "openai://gpt-4@sk-test@api.openai.com"},
					{Name: "gpt-3.5", URI: "openai://gpt-3.5-turbo@api.openai.com"},
				},
			},
		},
		{
			name: "legacy format with providers wrapper",
			content: `{
				"providers": {
					"anthropic": [
						{"uri": "anthropic://claude-3@sk-test@api.anthropic.com"}
					]
				}
			}`,
			want: map[string][]ModelEntry{
				"anthropic": {
					{Name: "claude-3", URI: "anthropic://claude-3@sk-test@api.anthropic.com"},
				},
			},
		},
		{
			name: "legacy format without wrapper",
			content: `{
				"openai": [
					{"uri": "openai://gpt-4@sk-test@api.openai.com"}
				]
			}`,
			want: map[string][]ModelEntry{
				"openai": {
					{Name: "gpt-4", URI: "openai://gpt-4@sk-test@api.openai.com"},
				},
			},
		},
		{
			name:    "invalid JSON",
			content: `{invalid json`,
			wantErr: true,
		},
		{
			name:    "empty object",
			content: `{}`,
			want:    map[string][]ModelEntry{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			if tt.content == "" {
				// Test file not found
				path = filepath.Join(t.TempDir(), "nonexistent.json")
			} else {
				path = filepath.Join(t.TempDir(), "model.json")
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			got, err := LoadConfig(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("LoadConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if len(got) != len(tt.want) {
				t.Fatalf("got %d providers, want %d", len(got), len(tt.want))
			}
			// Collect all entries across providers and compare as sets (map iteration is non-deterministic)
			var gotAll, wantAll []ModelEntry
			for _, entries := range got {
				gotAll = append(gotAll, entries...)
			}
			for _, entries := range tt.want {
				wantAll = append(wantAll, entries...)
			}
			if len(gotAll) != len(wantAll) {
				t.Fatalf("got %d total entries, want %d", len(gotAll), len(wantAll))
			}
			// Sort both for comparison
			sort.Slice(gotAll, func(i, j int) bool { return gotAll[i].Name < gotAll[j].Name })
			sort.Slice(wantAll, func(i, j int) bool { return wantAll[i].Name < wantAll[j].Name })
			for i := range gotAll {
				if gotAll[i].Name != wantAll[i].Name {
					t.Errorf("[%d] Name = %q, want %q", i, gotAll[i].Name, wantAll[i].Name)
				}
				if gotAll[i].URI != wantAll[i].URI {
					t.Errorf("[%d] URI = %q, want %q", i, gotAll[i].URI, wantAll[i].URI)
				}
			}
		})
	}
}

// =============================================================================
// MustLoadConfig tests
// =============================================================================

func TestMustLoadConfig(t *testing.T) {
	// Non-existent file should return empty map, not panic
	got := MustLoadConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if got == nil {
		t.Fatal("MustLoadConfig returned nil, want empty map")
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}

	// Valid file
	path := filepath.Join(t.TempDir(), "model.json")
	content := `{"openai": {"gpt-4": {"uri": "openai://gpt-4@api.openai.com"}}}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	got = MustLoadConfig(path)
	if len(got) != 1 {
		t.Errorf("got %d providers, want 1", len(got))
	}
}

// =============================================================================
// parseURI additional tests
// =============================================================================

func TestParseURI_table(t *testing.T) {
	tests := []struct {
		name        string
		uri         string
		wantAPIType string
		wantToken   string
		wantHost    string
		wantPath    string
		wantErr     bool
	}{
		{
			name:        "openai without token",
			uri:         "openai://gpt-4@api.openai.com",
			wantAPIType: "openai",
			wantToken:   "",
			wantHost:    "https://api.openai.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:        "openai with token",
			uri:         "openai://gpt-4@sk-test@api.openai.com",
			wantAPIType: "openai",
			wantToken:   "sk-test",
			wantHost:    "https://api.openai.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:        "anthropic without token",
			uri:         "anthropic://claude-sonnet-4@api.anthropic.com",
			wantAPIType: "anthropic",
			wantToken:   "",
			wantHost:    "https://api.anthropic.com",
			wantPath:    "/v1/messages",
		},
		{
			name:        "anthropic with token",
			uri:         "anthropic://claude-sonnet-4@sk-ant-test@api.anthropic.com",
			wantAPIType: "anthropic",
			wantToken:   "sk-ant-test",
			wantHost:    "https://api.anthropic.com",
			wantPath:    "/v1/messages",
		},
		{
			name:        "gemini",
			uri:         "gemini://gemini-2.0-flash@generativelanguage.googleapis.com",
			wantAPIType: "gemini",
			wantToken:   "",
			wantHost:    "https://generativelanguage.googleapis.com",
			wantPath:    "",
		},
		{
			name:        "unknown api type defaults to openai",
			uri:         "custom://model@api.custom.com",
			wantAPIType: "openai",
			wantToken:   "",
			wantHost:    "https://api.custom.com",
			wantPath:    "/v1/chat/completions",
		},
		{
			name:    "invalid - no scheme",
			uri:     "gpt-4@api.openai.com",
			wantErr: true,
		},
		{
			name:    "invalid - empty string",
			uri:     "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiType, token, baseURL, endpointPath, err := parseURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseURI() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if apiType != tt.wantAPIType {
				t.Errorf("apiType = %q, want %q", apiType, tt.wantAPIType)
			}
			if token != tt.wantToken {
				t.Errorf("token = %q, want %q", token, tt.wantToken)
			}
			if baseURL != tt.wantHost {
				t.Errorf("baseURL = %q, want %q", baseURL, tt.wantHost)
			}
			if endpointPath != tt.wantPath {
				t.Errorf("endpointPath = %q, want %q", endpointPath, tt.wantPath)
			}
		})
	}
}

// =============================================================================
// parseModel tests
// =============================================================================

func TestParseModel(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{"openai model", "openai://gpt-4@api.openai.com", "gpt-4"},
		{"anthropic model", "anthropic://claude-3@api.anthropic.com", "claude-3"},
		{"with token", "openai://gpt-4@sk-test@api.openai.com", "gpt-4"},
		{"invalid uri", "not-a-uri", ""},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseModel(tt.uri)
			if got != tt.want {
				t.Errorf("parseModel(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}
