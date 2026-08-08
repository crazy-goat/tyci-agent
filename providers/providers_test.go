package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/stream"
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

// =============================================================================
// Catalog tests
// =============================================================================

// catalogStub is a minimal Provider for exercising the catalog. It never
// streams; only the metadata methods matter here.
type catalogStub struct {
	name       string
	configured bool
	models     []string
}

func (s *catalogStub) Name() string       { return s.name }
func (s *catalogStub) IsConfigured() bool { return s.configured }
func (s *catalogStub) Models() []string   { return s.models }

// Client returns a client that carries an identity and nothing else — the
// catalog tests never send a request.
func (s *catalogStub) Client(model string) connector.ModelClient {
	return stubClient{name: s.name, model: model}
}

type stubClient struct{ name, model string }

func (c stubClient) Provider() string { return c.name }
func (c stubClient) Model() string    { return c.model }
func (c stubClient) Stream(context.Context, connector.Request) (<-chan stream.Event, error) {
	return nil, errors.New("stubClient does not stream")
}

// A Catalog is a value: two of them share nothing, so a test that registers a
// provider cannot leak into another test (or into Default).
func TestCatalog_IsolatedFromDefault(t *testing.T) {
	c := NewCatalog()
	c.Register(&catalogStub{name: "catalog-isolated-provider"})

	if _, ok := c.GetProvider("catalog-isolated-provider"); !ok {
		t.Fatal("provider missing from the catalog it was registered in")
	}
	if _, ok := Default.GetProvider("catalog-isolated-provider"); ok {
		t.Error("registering into a local Catalog leaked into Default")
	}
	if _, ok := NewCatalog().GetProvider("catalog-isolated-provider"); ok {
		t.Error("two Catalog values share state")
	}
}

// Register replaces an entry with the same name rather than accumulating.
func TestCatalog_RegisterReplacesSameName(t *testing.T) {
	c := NewCatalog()
	c.Register(&catalogStub{name: "dup", models: []string{"old"}})
	c.Register(&catalogStub{name: "dup", models: []string{"new"}})

	list := c.ListProviders()
	if len(list) != 1 {
		t.Fatalf("got %d providers, want 1", len(list))
	}
	if got := list[0].Models(); len(got) != 1 || got[0] != "new" {
		t.Errorf("models = %v, want [new] (second Register did not replace the first)", got)
	}
}

// ListProviders is sorted by name so CLI output is stable.
func TestCatalog_ListProvidersSorted(t *testing.T) {
	c := NewCatalog()
	for _, n := range []string{"zeta", "alpha", "mu"} {
		c.Register(&catalogStub{name: n})
	}
	var got []string
	for _, p := range c.ListProviders() {
		got = append(got, p.Name())
	}
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// FindModel: "provider/model" resolves by exact provider name and does not
// care whether the provider is configured; an unknown prefix fails.
func TestCatalog_FindModelQualified(t *testing.T) {
	c := NewCatalog()
	c.Register(&catalogStub{name: "prov", models: []string{"m1"}})

	p, m, ok := c.FindModel("prov/m1")
	if !ok || p.Name() != "prov" || m != "m1" {
		t.Fatalf("FindModel(prov/m1) = %v, %q, %v", p, m, ok)
	}
	// The bare name after the slash is passed through unvalidated — today's
	// behaviour, relied on by `--model provider/anything`.
	if _, m, ok := c.FindModel("prov/not-listed"); !ok || m != "not-listed" {
		t.Errorf("FindModel(prov/not-listed) = %q, %v; want pass-through", m, ok)
	}
	if _, _, ok := c.FindModel("nope/m1"); ok {
		t.Error("FindModel resolved an unknown provider prefix")
	}
}

// FindModel: a bare name matches a CONFIGURED provider's Models() and nothing
// else. There is no second pass — the FreeModels() fall-through was removed
// because no non-test implementation ever returned anything from it.
func TestCatalog_FindModelBareName(t *testing.T) {
	c := NewCatalog()
	c.Register(&catalogStub{name: "unconfigured", models: []string{"paid-model"}})
	c.Register(&catalogStub{name: "configured", configured: true, models: []string{"live-model"}})

	if p, m, ok := c.FindModel("live-model"); !ok || p.Name() != "configured" || m != "live-model" {
		t.Errorf("FindModel(live-model) = %v, %q, %v", p, m, ok)
	}
	if _, _, ok := c.FindModel("paid-model"); ok {
		t.Error("FindModel matched a model on an unconfigured provider")
	}
	if _, _, ok := c.FindModel("nothing-like-this"); ok {
		t.Error("FindModel matched a model nobody lists")
	}
}

// The package-level helpers must be wrappers over Default, not a second map.
func TestPackageLevelHelpersUseDefault(t *testing.T) {
	const name = "package-level-wrapper-probe"
	Register(&catalogStub{name: name, configured: true, models: []string{"probe-model"}})
	t.Cleanup(func() { delete(Default.providers, name) })

	if _, ok := Default.GetProvider(name); !ok {
		t.Fatal("Register() did not write into Default")
	}
	if _, ok := GetProvider(name); !ok {
		t.Error("GetProvider() did not read from Default")
	}
	if p, m, ok := FindModel(name + "/probe-model"); !ok || p.Name() != name || m != "probe-model" {
		t.Error("FindModel() did not read from Default")
	}
	var found bool
	for _, p := range ListProviders() {
		if p.Name() == name {
			found = true
		}
	}
	if !found {
		t.Error("ListProviders() did not read from Default")
	}
}
