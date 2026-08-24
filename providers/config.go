package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/internal/tyciconfig"
	"github.com/decodo/tyci/stream"
)

// uriEntry is the inner JSON object with just a URI.
type uriEntry struct {
	URI string `json:"uri"`
}

// LoadConfig reads model.json and returns parsed provider model entries.
// Accepts:
//   - {"provider": {"friendly-name": {"uri": "..."}}}  (new format)
//   - {"providers": {"name": [{"uri": "..."}]}}         (legacy)
//   - {"name": [{"uri": "..."}]}                        (legacy)
func LoadConfig(path string) (map[string][]ModelEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// New format: {"provider": {"name": {"uri": "..."}}}
	var newFmt map[string]map[string]uriEntry
	if err := json.Unmarshal(data, &newFmt); err == nil && len(newFmt) > 0 {
		result := make(map[string][]ModelEntry)
		for prov, models := range newFmt {
			for name, entry := range models {
				result[prov] = append(result[prov], ModelEntry{Name: name, URI: entry.URI})
			}
		}
		return result, nil
	}

	// Legacy format with "providers" wrapper
	var cfg struct {
		Providers map[string][]struct {
			URI string `json:"uri"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &cfg); err == nil && cfg.Providers != nil {
		result := make(map[string][]ModelEntry)
		for prov, list := range cfg.Providers {
			for _, e := range list {
				result[prov] = append(result[prov], ModelEntry{Name: parseModel(e.URI), URI: e.URI})
			}
		}
		return result, nil
	}

	// Legacy format without wrapper
	var legacy map[string][]struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	result := make(map[string][]ModelEntry)
	for prov, list := range legacy {
		for _, e := range list {
			result[prov] = append(result[prov], ModelEntry{Name: parseModel(e.URI), URI: e.URI})
		}
	}
	return result, nil
}

// MustLoadConfig loads config or returns empty map on error.
func MustLoadConfig(path string) map[string][]ModelEntry {
	cfg, err := LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: config error: %v\n", err)
	}
	if cfg == nil {
		cfg = make(map[string][]ModelEntry)
	}
	return cfg
}

// MergeModelEntries unions two model.json-shaped maps, keyed by
// "provider group name" -> "model name": every (group, name) pair from
// either side ends up available, and where both sides define the same pair,
// local wins. This is model.json's project-local semantics (TODO.md item
// 22): a custom provider/model catalog is a set of independent named
// entries, the same shape as mcp.json's server map, so it gets the same
// union-with-local-precedence treatment rather than config.json's per-field
// merge (there is no single "the" model.json field to override).
func MergeModelEntries(global, local map[string][]ModelEntry) map[string][]ModelEntry {
	type key struct{ group, name string }
	order := []key{}
	byKey := map[key]ModelEntry{}
	groupOf := map[key]string{}

	add := func(src map[string][]ModelEntry) {
		for group, list := range src {
			for _, e := range list {
				k := key{group, e.Name}
				if _, seen := byKey[k]; !seen {
					order = append(order, k)
				}
				byKey[k] = e
				groupOf[k] = group
			}
		}
	}
	add(global)
	add(local) // added second, so it overwrites on a (group, name) collision

	result := make(map[string][]ModelEntry)
	for _, k := range order {
		result[groupOf[k]] = append(result[groupOf[k]], byKey[k])
	}
	return result
}

// RegisterProvidersFromConfig reads model.json and registers a dynamic provider
// for each group found.
func RegisterProvidersFromConfig(path string) {
	entries := MustLoadConfig(path)
	for groupName, list := range entries {
		if len(list) == 0 {
			continue
		}
		Register(NewProvider(groupName, list, Deps{}))
	}
}

// RegisterProvidersFromConfigMerged reads the global model.json at
// globalPath and, when localPath is non-empty, a project-local override
// too, unions them (MergeModelEntries: local wins on a (group, model name)
// collision), and registers a dynamic provider per merged group. Not
// trust-gated: model.json only names custom model URIs, the same
// data-only posture as config.json, not something a project directory
// makes tyci execute.
func RegisterProvidersFromConfigMerged(globalPath, localPath string) {
	global := MustLoadConfig(globalPath)
	merged := global
	if localPath != "" {
		local := MustLoadConfig(localPath)
		merged = MergeModelEntries(global, local)
	}
	for groupName, list := range merged {
		if len(list) == 0 {
			continue
		}
		Register(NewProvider(groupName, list, Deps{}))
	}
}

// npmToAPIType maps npm package names to tyci API types.
var npmToAPIType = map[string]string{
	"@ai-sdk/openai":            "openai",
	"@ai-sdk/anthropic":         "anthropic",
	"@ai-sdk/gemini":            "gemini",
	"@ai-sdk/openai-compatible": "openai",
}

// npmToHost maps npm package names to default API hosts.
var npmToHost = map[string]string{
	"@ai-sdk/openai":    "api.openai.com",
	"@ai-sdk/anthropic": "api.anthropic.com",
	"@ai-sdk/gemini":    "generativelanguage.googleapis.com",
}

// LoadProvidersJSON reads a models.dev api.json format file (cached at
// providers.json) and returns parsed provider model entries with URIs.
func LoadProvidersJSON(path string) (map[string][]ModelEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading providers.json: %w", err)
	}

	var catalog map[string]connect.ModelsDevProvider
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("parsing providers.json: %w", err)
	}

	result := make(map[string][]ModelEntry)
	for id, p := range catalog {
		apiType, ok := npmToAPIType[p.NPM]
		if !ok {
			continue
		}
		host, apiPath := "", ""
		if p.API != "" {
			host, apiPath = splitHostPath(p.API)
		} else {
			host = npmToHost[p.NPM]
		}
		if host == "" {
			continue
		}
		for mid := range p.Models {
			uri := tyciconfig.ProviderURI{
				APIType:   apiType,
				Model:     mid,
				AuthToken: "",
				Host:      host,
				Path:      apiPath,
			}
			result[id] = append(result[id], ModelEntry{Name: mid, URI: uri.String()})
		}
	}
	return result, nil
}

// RegisterProvidersFromProvidersJSON reads providers.json (models.dev api.json
// format) and registers a dynamic provider for each entry.
func RegisterProvidersFromProvidersJSON(path string) error {
	entries, err := LoadProvidersJSON(path)
	if err != nil {
		return err
	}
	for groupName, list := range entries {
		if len(list) == 0 {
			continue
		}
		Register(NewProvider(groupName, list, Deps{}))
	}
	return nil
}

func splitHostPath(apiURL string) (host, path string) {
	if idx := strings.Index(apiURL, "://"); idx >= 0 {
		apiURL = apiURL[idx+3:]
	}
	if idx := strings.Index(apiURL, "/"); idx >= 0 {
		host = apiURL[:idx]
		path = apiURL[idx:]
	} else {
		host = apiURL
	}
	return host, path
}

// ModelEntry holds a single model's friendly name and URI.
type ModelEntry struct {
	Name string
	URI  string
}

// defaultConnectors is the connector set every dynamicProvider uses unless it
// carries its own. The registry is a value, so this is a package default —
// not a global registry the connector package would have to own.
var defaultConnectors = connector.DefaultRegistry()

// Deps are the collaborators a provider carries explicitly instead of
// reaching for package globals. Every field is optional; a zero Deps yields
// exactly the production defaults.
type Deps struct {
	// Auth resolves the provider-level credential. nil means DefaultAuth()
	// (auth.json, then environment).
	Auth AuthSource
	// Connectors is the set of wire protocols this provider may build. nil
	// means the built-in registry for this binary.
	Connectors *connector.Registry
	// HTTP is the client every connector this provider builds will send with.
	// nil means the api layer's shared default client — that is the normal
	// production case; only a caller that needs its own connection pool (the
	// subagent) or its own transport (tests) fills this in.
	HTTP connector.HTTPDoer
}

// NewProvider builds a Provider serving the given model catalog.
//
// This is the only constructor: everything the provider needs at request time
// arrives through it, so nothing in the request path consults a global.
func NewProvider(name string, entries []ModelEntry, deps Deps) Provider {
	return newDynamicProvider(name, entries, deps)
}

func newDynamicProvider(name string, entries []ModelEntry, deps Deps) *dynamicProvider {
	if deps.Auth == nil {
		deps.Auth = defaultAuthSource
	}
	if deps.Connectors == nil {
		deps.Connectors = defaultConnectors
	}
	// deps.HTTP stays nil on purpose: "no client of my own" is a meaningful
	// value that hands the choice to the api layer's shared default client.
	return &dynamicProvider{
		name:     name,
		entries:  entries,
		registry: deps.Connectors,
		auth:     deps.Auth,
		http:     deps.HTTP,
	}
}

// dynamicProvider implements Provider using config entries.
type dynamicProvider struct {
	name    string
	entries []ModelEntry
	// registry is optional; nil means defaultConnectors. NewProvider always
	// fills it, but hand-built literals (tests) may leave it zero.
	registry *connector.Registry
	// auth is optional; nil means defaultAuthSource.
	auth AuthSource
	// http is the client injected into every Endpoint this provider builds.
	// nil is meaningful — see Deps.HTTP.
	http connector.HTTPDoer
}

// withHTTP returns a copy of the provider bound to h. It returns a copy rather
// than mutating: a single provider value is shared by every parallel subagent,
// so mutating it here would let one child's connection pool leak into another.
//
// It is unexported and returns the concrete type. Transport is not part of the
// Provider contract — the catalog has no business knowing HTTP exists — and
// the only caller is modelClient.WithHTTP, inside this package, which is how
// the injection stays a compiler-checked hop rather than a type assertion that
// can quietly not match.
func (p *dynamicProvider) withHTTP(h connector.HTTPDoer) *dynamicProvider {
	c := *p
	c.http = h
	return &c
}

func (p *dynamicProvider) Name() string { return p.name }

func (p *dynamicProvider) IsConfigured() bool {
	for _, e := range p.entries {
		// The URI token is checked RAW, not through LiteralAuth: an entry
		// carrying an unresolvable "$FOO" still counts as configured here,
		// while Stream would refuse it. That asymmetry is pre-existing and
		// deliberately preserved — the `provider list` output must not start
		// hiding providers the user did configure. (ConfigWarnings surfaces
		// the unresolvable case separately; see providers/provider.go.)
		_, token, _, _, err := parseURI(e.URI)
		if err == nil && token != "" {
			return true
		}
	}
	// Loop-invariant: p.authSource().Key(p.name) depends only on p.name, never
	// on the entry e, so evaluating it once per entry (the old shape) bought
	// nothing but repeated auth.json reads — up to len(p.entries) of them for
	// a provider with no key at all. Every entry that DOES carry a URI token
	// already returned above without touching authSource, so moving this
	// below the loop costs at most one auth.json read per provider (zero for
	// a provider whose first entry has a token), instead of one per model.
	return p.authSource().Key(p.name) != ""
}

// ConfigWarnings reports URI tokens that look like "$FOO" but do not resolve
// through the environment right now. IsConfigured deliberately does not
// downgrade such an entry to "not configured" (see the comment there); this
// is where the same fact surfaces without changing that verdict.
//
// Only URI tokens are inspected. A literal "$FOO" stored in auth.json via
// hand-editing the file (rather than `provider auth set`, which already
// rejects an unresolvable "$FOO" at write time) would be an equally silent
// footgun, but AuthFile.Key only returns the resolved value — by the time it
// reaches here, an unresolved auth.json reference is indistinguishable from
// "no key at all". Surfacing that too would require AuthSource itself to stop
// returning a bare string, which is out of scope here.
//
// The result is deduplicated (many model entries commonly share one env var)
// and sorted (so `provider list` output — and this method — never flickers
// between runs due to map/slice iteration order).
func (p *dynamicProvider) ConfigWarnings() []string {
	seen := make(map[string]bool)
	var vars []string
	for _, e := range p.entries {
		_, token, _, _, err := parseURI(e.URI)
		if err != nil || !connect.LooksLikeEnvRef(token) {
			continue
		}
		if connect.ResolveToken(token) != "" {
			continue
		}
		name := strings.TrimPrefix(token, "$")
		if seen[name] {
			continue
		}
		seen[name] = true
		vars = append(vars, name)
	}
	if vars == nil {
		return nil
	}
	sort.Strings(vars)
	return vars
}

func (p *dynamicProvider) Models() []string {
	var models []string
	for _, e := range p.entries {
		if e.Name != "" {
			models = append(models, e.Name)
		}
	}
	return models
}

func (p *dynamicProvider) Stream(ctx context.Context, req Request) (<-chan stream.Event, error) {
	entry := p.findEntry(req.Model)
	if entry == nil {
		return nil, fmt.Errorf("model %q not found in provider %q", req.Model, p.name)
	}

	apiType, uriKey, baseURL, endpointPath, err := parseURI(entry.URI)
	if err != nil {
		return nil, err
	}

	apiKey, err := p.resolveAPIKey(uriKey)
	if err != nil {
		return nil, err
	}

	kind, err := p.kindFor(apiType)
	if err != nil {
		return nil, err
	}

	conn, err := p.connectors().New(kind, connector.Endpoint{
		BaseURL: baseURL,
		Path:    endpointPath,
		APIKey:  apiKey,
		HTTP:    p.http,
		Options: uriOptions(entry.URI),
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan stream.Event, 64)
	go func() {
		defer close(ch)
		if err := conn.Stream(ctx, req, forward(ch, ctx)); err != nil {
			select {
			case ch <- stream.StreamError{Err: err}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// findEntry returns the entry for a model name, or nil.
func (p *dynamicProvider) findEntry(model string) *ModelEntry {
	for i := range p.entries {
		if p.entries[i].Name == model {
			return &p.entries[i]
		}
	}
	return nil
}

// connectors returns the registry to build connectors from. NewProvider fills
// the field from Deps.Connectors (production: the built-in set; tests: their
// own registry of fakes); the nil fallback only covers hand-built literals.
func (p *dynamicProvider) connectors() *connector.Registry {
	if p.registry != nil {
		return p.registry
	}
	return defaultConnectors
}

// authSource returns the credential lookup for this provider, defaulting to
// auth.json + environment. Both IsConfigured and resolveAPIKey go through it,
// so there is exactly one description of the precedence.
func (p *dynamicProvider) authSource() AuthSource {
	if p.auth != nil {
		return p.auth
	}
	return defaultAuthSource
}

// kindFor maps a URI api_type to a connector kind.
//
// It deliberately does NOT fall back to the OpenAI connector. The `default:`
// branch of the old switch in Stream was already dead: tyciconfig.Parse
// normalizes every unrecognized URI scheme to "openai"
// (internal/tyciconfig/uri.go), and LoadProvidersJSON only ever produces the
// three known api_types — so apiType arriving here is always a known kind.
// A fallback would only ever fire for a kind that IS known but is missing from
// the registry (a build without anthropic/gemini), and quietly sending an
// Anthropic-shaped request to a chat-completions endpoint is far worse than
// the error the api/*_stub.go files used to return.
func (p *dynamicProvider) kindFor(apiType string) (string, error) {
	if p.connectors().Has(apiType) {
		return apiType, nil
	}
	if connector.IsKnownKind(apiType) {
		return "", connector.ErrExcluded(apiType)
	}
	return "", fmt.Errorf("unsupported api_type %q", apiType)
}

// uriOptions extracts the connector options encoded in the URI query string.
// Returns nil when there is nothing to pass.
func uriOptions(uri string) map[string]string {
	parsed, err := tyciconfig.Parse(uri)
	if err != nil || (!parsed.Reasoning && parsed.ReasoningEffort == "") {
		return nil
	}
	options := make(map[string]string)
	if parsed.Reasoning {
		options[connector.OptReasoning] = "true"
	}
	if parsed.ReasoningEffort != "" {
		options[connector.OptReasoningEffort] = parsed.ReasoningEffort
	}
	return options
}

// resolveAPIKey resolves the credential for this provider: the token from the
// URI first, then whatever the provider's AuthSource offers (by default
// auth.json, then env vars).
func (p *dynamicProvider) resolveAPIKey(uriKey string) (string, error) {
	if apiKey := (AuthChain{LiteralAuth(uriKey), p.authSource()}).Key(p.name); apiKey != "" {
		return apiKey, nil
	}
	// uriKey looking like "$FOO" and failing to resolve is a DIFFERENT failure
	// than "no credential configured at all" (ConfigWarnings already flagged
	// it in `provider list`, before any request was sent) — name the variable
	// instead of implying the user never set a key.
	if connect.LooksLikeEnvRef(uriKey) {
		return "", fmt.Errorf("%s is set to %q but env var %s is empty or unset", p.name, uriKey, strings.TrimPrefix(uriKey, "$"))
	}
	return "", fmt.Errorf("no API key for %q (set via 'tyci provider auth set', %s_API_KEY env var, OPENCODE_API_KEY, or use a free model)", p.name, strings.ToUpper(p.name))
}

// forward creates an emit function for stream events.
func forward(ch chan<- stream.Event, ctx context.Context) func(stream.Event) error {
	return func(e stream.Event) error {
		select {
		case ch <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// parseURI parses the URI using the shared tyciconfig.ProviderURI type.
// Kept for backward compatibility with existing callers.
func parseURI(uri string) (apiType, authToken, baseURL, endpointPath string, err error) {
	u, err := tyciconfig.Parse(uri)
	if err != nil {
		return "", "", "", "", err
	}

	apiType = u.APIType
	if u.Protocol != "" {
		apiType = u.Protocol
	}

	endpointPath = u.Path
	switch apiType {
	case "anthropic":
		endpointPath = appendChatPath(endpointPath, "/v1/messages")
	case "gemini":
		// Gemini uses different path structure
	case "responses":
		endpointPath = appendChatPath(endpointPath, "/v1/responses")
	default:
		endpointPath = appendChatPath(endpointPath, "/v1/chat/completions")
	}

	return apiType, u.AuthToken, "https://" + u.Host, endpointPath, nil
}

// appendChatPath appends the API-specific chat endpoint path to the base path.
// It preserves a non-empty base path (e.g. /zen/go/v1) and only adds the
// default endpoint when the base path is empty or ends with /v1.
func appendChatPath(basePath, defaultEndpoint string) string {
	basePath = strings.TrimSuffix(basePath, "/")
	if basePath == "" {
		return defaultEndpoint
	}
	if strings.HasSuffix(basePath, "/v1") {
		return basePath + strings.TrimPrefix(defaultEndpoint, "/v1")
	}
	return basePath
}

// parseModel extracts the model name from a URI.
func parseModel(uri string) string {
	u, err := tyciconfig.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Model
}
