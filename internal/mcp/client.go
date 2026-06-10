package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/decodo/tyci-agent/internal/connect"
)

// Client defines the interface for MCP servers.
type Client interface {
	// Initialize performs the MCP handshake.
	Initialize(ctx context.Context) error

	// ListTools returns available tools from the server.
	ListTools(ctx context.Context) ([]Tool, error)

	// CallTool invokes a tool on the server.
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (*CallToolResult, error)

	// Close shuts down the client and cleans up resources.
	Close() error

	// Name returns the server name.
	Name() string
}

// ServerConfig represents a single MCP server configuration.
type ServerConfig struct {
	Command string      `json:"command,omitempty"`
	Args    []string    `json:"args,omitempty"`
	URL     string      `json:"url,omitempty"`
	Auth    interface{} `json:"auth,omitempty"` // string (legacy) or AuthConfig object
}

// GetAuthConfig returns the AuthConfig for this server.
// Supports both legacy string format ("bearer") and new AuthConfig object.
func (s *ServerConfig) GetAuthConfig() *AuthConfig {
	if s.Auth == nil {
		return nil
	}

	// Legacy string format
	if authStr, ok := s.Auth.(string); ok {
		return &AuthConfig{Type: authStr}
	}

	// New object format - unmarshal from map
	if authMap, ok := s.Auth.(map[string]interface{}); ok {
		cfg := &AuthConfig{}
		if v, ok := authMap["type"].(string); ok {
			cfg.Type = v
		}
		if v, ok := authMap["token_env"].(string); ok {
			cfg.TokenEnv = v
		}
		if v, ok := authMap["token"].(string); ok {
			cfg.Token = v
		}
		return cfg
	}

	return nil
}

// GetAuthType returns the auth type string for legacy compatibility.
func (s *ServerConfig) GetAuthType() string {
	if s.Auth == nil {
		return ""
	}
	if authStr, ok := s.Auth.(string); ok {
		return authStr
	}
	if cfg := s.GetAuthConfig(); cfg != nil {
		return cfg.Type
	}
	return ""
}

// Config represents the MCP configuration file.
type Config struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// ConfigPath returns the path to mcp.json.
func ConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			home = "/tmp"
		}
	}
	return filepath.Join(home, ".tyci", "mcp.json")
}

// LoadConfig reads the MCP configuration file.
func LoadConfig() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{MCPServers: make(map[string]ServerConfig)}, nil
		}
		return nil, fmt.Errorf("reading mcp config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing mcp config: %w", err)
	}

	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]ServerConfig)
	}

	return &cfg, nil
}

// SaveConfig writes the MCP configuration file.
func SaveConfig(cfg *Config) error {
	path := ConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding mcp config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing mcp config: %w", err)
	}

	return nil
}

// NewClient creates a new MCP client based on the server config.
// For stdio servers (with Command), it returns a StdioClient.
// For HTTP servers (with URL), it returns an error (not yet implemented).
func NewClient(name string, cfg ServerConfig) (Client, error) {
	if cfg.Command != "" {
		return NewStdioClient(name, cfg.Command, cfg.Args), nil
	}
	if cfg.URL != "" {
		hc := NewHTTPClient(name, cfg.URL, cfg.GetAuthType())
		return hc, nil
	}
	return nil, fmt.Errorf("invalid server config: must have command or url")
}

// ConnectAll connects to all configured MCP servers and returns them.
func ConnectAll(ctx context.Context) (map[string]Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	// Initialize auth manager
	authMgr := NewAuthManager()
	if err := authMgr.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load MCP auth: %v\n", err)
	}

	clients := make(map[string]Client)
	for name, serverCfg := range cfg.MCPServers {
		client, err := NewClient(name, serverCfg)
		if err != nil {
			// Log warning but continue with other servers
			fmt.Fprintf(os.Stderr, "Warning: failed to create MCP client %q: %v\n", name, err)
			continue
		}

		if err := client.Initialize(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize MCP server %q: %v\n", name, err)
			client.Close()
			continue
		}

		// Resolve auth token using AuthManager
		authCfg := serverCfg.GetAuthConfig()
		if token := GetTokenForServer(authMgr, name, authCfg); token != "" {
			if hc, ok := client.(*HTTPClient); ok {
				hc.SetAuthToken(token)
			}
		}

		// Legacy fallback: check connect package
		if authCfg == nil {
			if token, ok, _ := connect.GetKey("mcp_" + name); ok && token != "" {
				if hc, ok := client.(*HTTPClient); ok {
					hc.SetAuthToken(token)
				}
			}
		}

		clients[name] = client
	}

	return clients, nil
}
