package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuthConfig represents authentication configuration for an MCP server.
type AuthConfig struct {
	Type     string `json:"type,omitempty"`      // "bearer", "none"
	TokenEnv string `json:"token_env,omitempty"` // env var name for token
	Token    string `json:"token,omitempty"`     // literal token (not recommended)
}

// TokenEntry represents a stored token with metadata.
type TokenEntry struct {
	Token     string    `json:"token"`
	Server    string    `json:"server"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// AuthManager manages authentication tokens for MCP servers.
type AuthManager struct {
	tokens    map[string]*TokenEntry // server name -> token
	tokenFile string
}

// NewAuthManager creates a new AuthManager.
func NewAuthManager() *AuthManager {
	return &AuthManager{
		tokens:    make(map[string]*TokenEntry),
		tokenFile: mcpAuthPath(),
	}
}

// mcpAuthPath returns the path to mcp_auth.json.
func mcpAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			home = "/tmp"
		}
	}
	return filepath.Join(home, ".tyci", "mcp_auth.json")
}

// Load reads the token store from disk.
func (am *AuthManager) Load() error {
	data, err := os.ReadFile(am.tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // empty store is fine
		}
		return fmt.Errorf("reading auth file: %w", err)
	}

	if err := json.Unmarshal(data, &am.tokens); err != nil {
		return fmt.Errorf("parsing auth file: %w", err)
	}

	return nil
}

// Save writes the token store to disk.
func (am *AuthManager) Save() error {
	dir := filepath.Dir(am.tokenFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating auth dir: %w", err)
	}

	data, err := json.MarshalIndent(am.tokens, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding auth file: %w", err)
	}

	if err := os.WriteFile(am.tokenFile, data, 0600); err != nil {
		return fmt.Errorf("writing auth file: %w", err)
	}

	return nil
}

// SetToken stores a token for the given server.
func (am *AuthManager) SetToken(server, token string) {
	am.tokens[server] = &TokenEntry{
		Token:     token,
		Server:    server,
		CreatedAt: time.Now(),
	}
}

// SetTokenWithExpiry stores a token with an expiration time.
func (am *AuthManager) SetTokenWithExpiry(server, token string, expiresAt time.Time) {
	am.tokens[server] = &TokenEntry{
		Token:     token,
		Server:    server,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
}

// GetToken retrieves the token for the given server.
// Returns the token and whether it was found.
func (am *AuthManager) GetToken(server string) (string, bool) {
	entry, ok := am.tokens[server]
	if !ok {
		return "", false
	}

	// Check if token is expired
	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt) {
		delete(am.tokens, server)
		return "", false
	}

	return entry.Token, true
}

// RemoveToken deletes the token for the given server.
func (am *AuthManager) RemoveToken(server string) {
	delete(am.tokens, server)
}

// ListServers returns the names of all servers with stored tokens.
func (am *AuthManager) ListServers() []string {
	servers := make([]string, 0, len(am.tokens))
	for server := range am.tokens {
		servers = append(servers, server)
	}
	return servers
}

// IsExpired checks if the token for the given server is expired.
func (am *AuthManager) IsExpired(server string) bool {
	entry, ok := am.tokens[server]
	if !ok {
		return true
	}
	if entry.ExpiresAt.IsZero() {
		return false // no expiry set
	}
	return time.Now().After(entry.ExpiresAt)
}

// GetTokenForServer resolves the auth token for an MCP server.
// It checks in order:
// 1. Token from AuthManager (stored token)
// 2. Token from environment variable (if token_env is set)
// 3. Token from AuthConfig (literal token)
func GetTokenForServer(am *AuthManager, serverName string, authCfg *AuthConfig) string {
	if authCfg == nil || authCfg.Type == "" || authCfg.Type == "none" {
		return ""
	}

	// 1. Check stored token first
	if token, ok := am.GetToken(serverName); ok && token != "" {
		return token
	}

	// 2. Check environment variable
	if authCfg.TokenEnv != "" {
		if token := os.Getenv(authCfg.TokenEnv); token != "" {
			// Store for future use
			am.SetToken(serverName, token)
			return token
		}
	}

	// 3. Check literal token in config
	if authCfg.Token != "" {
		am.SetToken(serverName, authCfg.Token)
		return authCfg.Token
	}

	return ""
}
