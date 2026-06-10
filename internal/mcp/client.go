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
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
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
		return nil, fmt.Errorf("HTTP MCP transport not yet implemented")
	}
	return nil, fmt.Errorf("invalid server config: must have command or url")
}

// ConnectAll connects to all configured MCP servers and returns them.
func ConnectAll(ctx context.Context) (map[string]Client, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
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

		// Add auth token if available
		if token, ok, _ := connect.GetKey("mcp_" + name); ok && token != "" {
			// Token will be used by HTTP client when implemented
			_ = token
		}

		clients[name] = client
	}

	return clients, nil
}
