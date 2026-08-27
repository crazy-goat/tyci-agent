package tyciconfig

import (
	"fmt"
	"strings"
)

// ProviderURI represents a provider model URI in the format:
//
//	api_type://model@auth_token@host:port/path
//
// api_type can be: openai, anthropic, gemini, responses (defaults to openai).
// The auth_token can be empty for free models.
// The optional query parameter ?api=responses selects the Responses API wire
// protocol for one model while keeping the provider scheme as openai.
// ?reasoning=true remains available for chat-completion requests. Responses
// models may use ?reasoning=xhigh (or another provider-supported effort).
// ?fallbacks=false is forwarded verbatim as a query parameter on the outgoing
// request URL; see the Fallbacks field doc below for what it does and does
// not control.
type ProviderURI struct {
	APIType         string // openai, anthropic, gemini, responses
	Protocol        string // optional wire-protocol override from ?api=...
	Model           string // model name (e.g., "gpt-4")
	AuthToken       string // optional auth token (can be empty or "$ENV_VAR")
	Host            string // host:port (e.g., "api.openai.com")
	Path            string // optional path (e.g., "/v1/chat/completions"), without query
	Reasoning       bool   // send "reasoning": true in chat requests
	ReasoningEffort string // Responses reasoning effort, e.g. "xhigh"

	// Fallbacks carries the raw value of ?fallbacks=... from the URI (e.g.
	// "false"), or "" when the option is absent. It is forwarded verbatim as
	// a ?fallbacks=... query parameter on the outgoing request URL — Nexos
	// Chat Completions and Responses read it to opt a request out of Nexos'
	// own server-side provider fallback.
	//
	// This is unrelated to tyci's, OpenCode's, or Pi's application-level
	// fallback lists (which model/provider to try next when a request
	// fails) — those remain configured separately wherever tyci resolves a
	// model chain. Setting ?fallbacks=false here only tells Nexos not to
	// silently reroute a single request to another upstream model on its
	// side; it has no effect on tyci's own retry/fallback behavior.
	Fallbacks string
}

// String returns the URI in the format: api_type://model@auth_token@host/path.
// Query options are emitted in a stable order.
func (u ProviderURI) String() string {
	s := fmt.Sprintf("%s://%s@%s@%s%s", u.APIType, u.Model, u.AuthToken, u.Host, u.Path)
	var query []string
	if u.Protocol != "" {
		query = append(query, "api="+u.Protocol)
	}
	if u.ReasoningEffort != "" {
		query = append(query, "reasoning="+u.ReasoningEffort)
	} else if u.Reasoning {
		query = append(query, "reasoning=true")
	}
	if u.Fallbacks != "" {
		query = append(query, "fallbacks="+u.Fallbacks)
	}
	if len(query) > 0 {
		s += "?" + strings.Join(query, "&")
	}
	return s
}

// Parse parses a URI string into a ProviderURI.
// Format: api_type://model@auth_token@host:port/path?api=responses&reasoning=xhigh
func Parse(uri string) (ProviderURI, error) {
	parts := strings.SplitN(uri, "://", 2)
	if len(parts) != 2 {
		return ProviderURI{}, fmt.Errorf("invalid URI: missing ://")
	}
	scheme := parts[0]
	rest := parts[1]

	apiType := scheme
	switch apiType {
	case "openai", "anthropic", "gemini", "responses":
		// known
	default:
		apiType = "openai"
	}

	// Split by '@' - format: model@token@hostportpath
	atParts := strings.SplitN(rest, "@", 3)
	if len(atParts) < 2 {
		return ProviderURI{}, fmt.Errorf("invalid URI: expected model@token@host:port/path, got %q", rest)
	}
	modelName := atParts[0]

	var authToken string
	var hostPortPath string
	if len(atParts) >= 3 {
		authToken = atParts[1]
		hostPortPath = atParts[2]
	} else {
		hostPortPath = atParts[1]
	}

	var host string
	var rawPath string
	if idx := strings.Index(hostPortPath, "/"); idx >= 0 {
		host = hostPortPath[:idx]
		rawPath = hostPortPath[idx:]
	} else {
		// No path — check for query string attached to host
		if qIdx := strings.Index(hostPortPath, "?"); qIdx >= 0 {
			host = hostPortPath[:qIdx]
			rawPath = hostPortPath[qIdx:]
		} else {
			host = hostPortPath
		}
	}

	// Parse query parameters from rawPath
	reasoning := false // default: don't send reasoning field
	reasoningEffort := ""
	protocol := ""
	fallbacks := ""
	path := rawPath
	if idx := strings.Index(rawPath, "?"); idx >= 0 {
		path = rawPath[:idx]
		query := rawPath[idx+1:]
		for _, param := range strings.Split(query, "&") {
			kv := strings.SplitN(param, "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "api", "protocol":
				protocol = kv[1]
			case "reasoning":
				switch kv[1] {
				case "true":
					reasoning = true
				case "false", "":
					// Explicitly disabled, or malformed without a value.
				default:
					reasoningEffort = kv[1]
				}
			case "reasoning_effort":
				reasoningEffort = kv[1]
			case "fallbacks":
				fallbacks = kv[1]
			}
		}
	}

	return ProviderURI{
		APIType:         apiType,
		Protocol:        protocol,
		Model:           modelName,
		AuthToken:       authToken,
		Host:            host,
		Path:            path,
		Reasoning:       reasoning,
		ReasoningEffort: reasoningEffort,
		Fallbacks:       fallbacks,
	}, nil
}
