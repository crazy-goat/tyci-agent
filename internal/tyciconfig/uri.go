package tyciconfig

import (
	"fmt"
	"strings"
)

// ProviderURI represents a provider model URI in the format:
//
//	api_type://model@auth_token@host:port/path
//
// api_type can be: openai, anthropic, gemini (defaults to openai).
// The auth_token can be empty for free models.
type ProviderURI struct {
	APIType  string // openai, anthropic, gemini
	Model    string // model name (e.g., "gpt-4")
	AuthToken string // optional auth token (can be empty or "$ENV_VAR")
	Host     string // host:port (e.g., "api.openai.com")
	Path     string // optional path (e.g., "/v1/chat/completions")
}

// String returns the URI in the format: api_type://model@auth_token@host/path
func (u ProviderURI) String() string {
	return fmt.Sprintf("%s://%s@%s@%s%s", u.APIType, u.Model, u.AuthToken, u.Host, u.Path)
}

// Parse parses a URI string into a ProviderURI.
// Format: api_type://model@auth_token@host:port/path
func Parse(uri string) (ProviderURI, error) {
	parts := strings.SplitN(uri, "://", 2)
	if len(parts) != 2 {
		return ProviderURI{}, fmt.Errorf("invalid URI: missing ://")
	}
	scheme := parts[0]
	rest := parts[1]

	apiType := scheme
	switch apiType {
	case "openai", "anthropic", "gemini":
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
	var path string
	if idx := strings.Index(hostPortPath, "/"); idx >= 0 {
		host = hostPortPath[:idx]
		path = hostPortPath[idx:]
	} else {
		host = hostPortPath
	}

	return ProviderURI{
		APIType:   apiType,
		Model:     modelName,
		AuthToken: authToken,
		Host:      host,
		Path:      path,
	}, nil
}

// FullEndpoint returns the complete endpoint URL including the default path for the API type.
func (u ProviderURI) FullEndpoint() string {
	endpointPath := u.Path
	switch u.APIType {
	case "anthropic":
		endpointPath += "/v1/messages"
	case "gemini":
		// Gemini uses different path structure
	default:
		endpointPath += "/v1/chat/completions"
	}
	return "https://" + u.Host + endpointPath
}
