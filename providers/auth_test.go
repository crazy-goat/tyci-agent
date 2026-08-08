package providers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeAuthJSON points HOME at a fresh temp dir and writes auth.json there.
// Returns the temp HOME.
func writeAuthJSON(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	_ = os.Setenv("HOME", dir)
	t.Cleanup(func() { _ = os.Setenv("HOME", origHome) })

	authDir := filepath.Join(dir, ".tyci")
	if err := os.MkdirAll(authDir, 0755); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLiteralAuth(t *testing.T) {
	if got := LiteralAuth("sk-plain").Key("any"); got != "sk-plain" {
		t.Errorf("plain literal = %q, want %q", got, "sk-plain")
	}
	if got := LiteralAuth("").Key("any"); got != "" {
		t.Errorf("empty literal = %q, want empty", got)
	}
	// "$VAR" is a reference, not a credential: it resolves or it is nothing.
	t.Setenv("TYCI_TEST_LITERAL_SRC", "resolved-value")
	if got := LiteralAuth("$TYCI_TEST_LITERAL_SRC").Key("any"); got != "resolved-value" {
		t.Errorf("env ref = %q, want %q", got, "resolved-value")
	}
	_ = os.Unsetenv("TYCI_TEST_LITERAL_MISSING")
	if got := LiteralAuth("$TYCI_TEST_LITERAL_MISSING").Key("any"); got != "" {
		t.Errorf("unresolvable env ref = %q, want empty", got)
	}
	// The provider name is irrelevant to a literal.
	if got := LiteralAuth("sk-plain").Key("some-other-provider"); got != "sk-plain" {
		t.Errorf("literal depended on provider name: %q", got)
	}
}

func TestAuthFile_KeyPresent(t *testing.T) {
	writeAuthJSON(t, `{"prov":"sk-from-file"}`)
	if got := (AuthFile{}).Key("prov"); got != "sk-from-file" {
		t.Errorf("Key = %q, want %q", got, "sk-from-file")
	}
	if got := (AuthFile{}).Key("other"); got != "" {
		t.Errorf("Key for absent provider = %q, want empty", got)
	}
}

func TestAuthFile_MissingFileIsSilent(t *testing.T) {
	writeAuthJSON(t, "") // no auth.json at all
	var warned []error
	src := AuthFile{Warn: func(err error) { warned = append(warned, err) }}
	if got := src.Key("prov"); got != "" {
		t.Errorf("Key = %q, want empty", got)
	}
	if len(warned) != 0 {
		t.Errorf("a missing auth.json warned %d times, want 0: %v", len(warned), warned)
	}
}

// A "$VAR" entry in auth.json resolves through the environment — the
// regression from `provider auth set nexos '$NEXOS_API_KEY'`.
func TestAuthFile_ResolvesEnvRef(t *testing.T) {
	writeAuthJSON(t, `{"nexos":"$TYCI_TEST_AUTHFILE_REF"}`)

	_ = os.Unsetenv("TYCI_TEST_AUTHFILE_REF")
	if got := (AuthFile{}).Key("nexos"); got != "" {
		t.Errorf("unresolvable $VAR = %q, want empty", got)
	}
	t.Setenv("TYCI_TEST_AUTHFILE_REF", "real-key")
	if got := (AuthFile{}).Key("nexos"); got != "real-key" {
		t.Errorf("$VAR = %q, want %q", got, "real-key")
	}
}

// A broken auth.json warns once and yields nothing — it must not abort the
// chain, so the env source still gets its turn.
func TestAuthFile_BrokenFileWarnsAndYieldsNothing(t *testing.T) {
	writeAuthJSON(t, `{not json`)
	var warned []error
	src := AuthFile{Warn: func(err error) { warned = append(warned, err) }}
	if got := src.Key("prov"); got != "" {
		t.Errorf("Key = %q, want empty", got)
	}
	if len(warned) != 1 {
		t.Fatalf("warned %d times, want 1", len(warned))
	}

	t.Setenv("PROV_API_KEY", "from-env")
	if got := (AuthChain{src, EnvAuth{}}).Key("prov"); got != "from-env" {
		t.Errorf("chain = %q, want %q — a broken auth.json swallowed the env source", got, "from-env")
	}
}

func TestEnvAuth_Precedence(t *testing.T) {
	_ = os.Unsetenv("PROV_API_KEY")
	_ = os.Unsetenv("OPENCODE_API_KEY")
	if got := (EnvAuth{}).Key("prov"); got != "" {
		t.Errorf("Key with nothing set = %q, want empty", got)
	}

	t.Setenv("OPENCODE_API_KEY", "shared")
	if got := (EnvAuth{}).Key("prov"); got != "shared" {
		t.Errorf("Key = %q, want %q", got, "shared")
	}

	// The provider-specific var wins over the shared one, and the name is
	// upper-cased (including provider names with a dash).
	t.Setenv("PROV_API_KEY", "specific")
	if got := (EnvAuth{}).Key("prov"); got != "specific" {
		t.Errorf("Key = %q, want %q", got, "specific")
	}
	t.Setenv("TEST-PROVIDER_API_KEY", "dashed")
	if got := (EnvAuth{}).Key("test-provider"); got != "dashed" {
		t.Errorf("Key = %q, want %q", got, "dashed")
	}
}

// Env values are literals: a "$VAR" sitting in an env var is NOT expanded
// again. (auth.json entries are; see TestAuthFile_ResolvesEnvRef.)
func TestEnvAuth_DoesNotExpandDollar(t *testing.T) {
	t.Setenv("TYCI_TEST_INNER", "inner-value")
	t.Setenv("PROV_API_KEY", "$TYCI_TEST_INNER")
	if got := (EnvAuth{}).Key("prov"); got != "$TYCI_TEST_INNER" {
		t.Errorf("Key = %q, want the literal %q", got, "$TYCI_TEST_INNER")
	}
}

// stubAuth records how often it was asked and answers with a fixed key.
type stubAuth struct {
	key   string
	calls int
	saw   []string
}

func (s *stubAuth) Key(provider string) string {
	s.calls++
	s.saw = append(s.saw, provider)
	return s.key
}

func TestAuthChain_FirstNonEmptyWinsAndShortCircuits(t *testing.T) {
	first := &stubAuth{key: ""}
	second := &stubAuth{key: "second-key"}
	third := &stubAuth{key: "third-key"}

	got := (AuthChain{first, nil, second, third}).Key("prov")
	if got != "second-key" {
		t.Errorf("chain = %q, want %q", got, "second-key")
	}
	if first.calls != 1 || second.calls != 1 {
		t.Errorf("calls: first=%d second=%d, want 1 and 1", first.calls, second.calls)
	}
	if third.calls != 0 {
		t.Errorf("third source was asked %d times after a hit, want 0", third.calls)
	}
	if len(second.saw) != 1 || second.saw[0] != "prov" {
		t.Errorf("provider name passed down = %v, want [prov]", second.saw)
	}
}

func TestAuthChain_EmptyAndAllEmpty(t *testing.T) {
	if got := (AuthChain{}).Key("prov"); got != "" {
		t.Errorf("empty chain = %q, want empty", got)
	}
	if got := (AuthChain{&stubAuth{}, &stubAuth{}}).Key("prov"); got != "" {
		t.Errorf("all-empty chain = %q, want empty", got)
	}
	// A chain is itself an AuthSource, so it nests.
	nested := AuthChain{AuthChain{&stubAuth{}}, AuthChain{&stubAuth{key: "deep"}}}
	if got := nested.Key("prov"); got != "deep" {
		t.Errorf("nested chain = %q, want %q", got, "deep")
	}
}

// DefaultAuth encodes the production precedence: auth.json, then environment.
func TestDefaultAuth_Precedence(t *testing.T) {
	writeAuthJSON(t, `{"prov":"sk-from-file"}`)
	t.Setenv("PROV_API_KEY", "sk-from-env")

	if got := DefaultAuth().Key("prov"); got != "sk-from-file" {
		t.Errorf("Key = %q, want %q (auth.json must outrank env)", got, "sk-from-file")
	}
	// Provider absent from auth.json falls through to env.
	if got := DefaultAuth().Key("other"); got != "" {
		t.Errorf("Key(other) = %q, want empty", got)
	}
	t.Setenv("OTHER_API_KEY", "sk-other-env")
	if got := DefaultAuth().Key("other"); got != "sk-other-env" {
		t.Errorf("Key(other) = %q, want %q", got, "sk-other-env")
	}
}

// resolveAPIKey and IsConfigured must consult the SAME injected AuthSource —
// there is no second, duplicated copy of the lookup anywhere.
func TestDynamicProvider_UsesInjectedAuthSource(t *testing.T) {
	writeAuthJSON(t, "") // guarantee auth.json cannot answer
	_ = os.Unsetenv("INJECTED_API_KEY")
	_ = os.Unsetenv("OPENCODE_API_KEY")

	src := &stubAuth{key: "injected-key"}
	p := &dynamicProvider{
		name:    "injected",
		entries: []ModelEntry{{Name: "m", URI: "openai://m@api.example.invalid"}},
		auth:    src,
	}

	if !p.IsConfigured() {
		t.Error("IsConfigured() = false; the injected AuthSource has a key")
	}
	got, err := p.resolveAPIKey("")
	if err != nil {
		t.Fatalf("resolveAPIKey: %v", err)
	}
	if got != "injected-key" {
		t.Errorf("resolveAPIKey = %q, want %q", got, "injected-key")
	}
	if src.calls < 2 {
		t.Errorf("injected source asked %d times, want at least 2 (once per caller)", src.calls)
	}
}

// The URI token still outranks the provider-level source.
func TestDynamicProvider_URITokenOutranksAuthSource(t *testing.T) {
	src := &stubAuth{key: "from-source"}
	p := &dynamicProvider{name: "prov", auth: src}

	got, err := p.resolveAPIKey("sk-from-uri")
	if err != nil {
		t.Fatalf("resolveAPIKey: %v", err)
	}
	if got != "sk-from-uri" {
		t.Errorf("resolveAPIKey = %q, want %q", got, "sk-from-uri")
	}
	if src.calls != 0 {
		t.Errorf("provider-level source was asked %d times despite a URI token, want 0", src.calls)
	}
}

// The exact error string is part of the CLI's contract with the user.
func TestDynamicProvider_ResolveAPIKeyErrorMessage(t *testing.T) {
	p := &dynamicProvider{name: "nexos", auth: &stubAuth{}}
	_, err := p.resolveAPIKey("")
	if err == nil {
		t.Fatal("resolveAPIKey returned no error with no credential anywhere")
	}
	want := `no API key for "nexos" (set via 'tyci provider auth set', NEXOS_API_KEY env var, OPENCODE_API_KEY, or use a free model)`
	if err.Error() != want {
		t.Errorf("error = %q,\nwant       %q", err.Error(), want)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("error unexpectedly wraps another: %v", errors.Unwrap(err))
	}
}
