package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/decodo/tyci/internal/connect"
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

	// SetSamplingHandler sets the handler for sampling/createMessage requests.
	SetSamplingHandler(handler SamplingHandler)

	// SetElicitationHandler sets the handler for elicitation/create requests.
	SetElicitationHandler(handler ElicitationHandler)
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

// ConnectAll connects to all configured MCP servers and returns them. It
// blocks on each server in turn with no bound on how long a hung handshake
// can take — ConnectAllTimeout is the production path (see tools.InitMCP);
// this is kept for direct/manual use where an unbounded wait is acceptable.
func ConnectAll(ctx context.Context) (map[string]Client, error) {
	servers, err := ConnectAllTimeout(ctx, 0)
	if err != nil {
		return nil, err
	}
	clients := make(map[string]Client, len(servers))
	for name, srv := range servers {
		clients[name] = srv.Client
	}
	return clients, nil
}

// ConnectedServer bundles a live, initialized MCP client with the tools it
// advertised when ConnectAllTimeout listed them.
type ConnectedServer struct {
	Client Client
	Tools  []Tool
}

// ConnectAllTimeout connects to every configured MCP server concurrently,
// giving each one up to timeout (0 means unbounded) to finish its handshake
// and list its tools. A server that errors, or doesn't respond in time, is
// skipped with one warning on stderr rather than failing the whole batch or
// blocking the others — a single missing, slow, or broken server must never
// keep tyci from starting, and must never keep a healthy server from being
// used.
//
// A server that answers after its timeout has already been given up on is
// closed as soon as it does, in the background, so a merely-slow (not
// actually hung) server doesn't sit around connected-but-unused for the
// rest of the session. A server that never answers at all leaks its
// process until ctx is canceled — callers that connect with a long-lived
// ctx (see tools.InitMCP) must cancel it on shutdown (tools.ShutdownMCP
// closes every server that DID make it into the returned map; ctx
// cancellation is what reaches the stragglers that didn't).
func ConnectAllTimeout(ctx context.Context, timeout time.Duration) (map[string]ConnectedServer, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}

	authMgr := NewAuthManager()
	if err := authMgr.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load MCP auth: %v\n", err)
	}

	result := make(map[string]ConnectedServer, len(cfg.MCPServers))
	if len(cfg.MCPServers) == 0 {
		return result, nil
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, serverCfg := range cfg.MCPServers {
		name, serverCfg := name, serverCfg
		wg.Add(1)
		go func() {
			defer wg.Done()

			type outcome struct {
				client Client
				tools  []Tool
				err    error
			}
			ch := make(chan outcome, 1)
			go func() {
				client, toolList, err := connectOne(ctx, name, serverCfg, authMgr)
				ch <- outcome{client: client, tools: toolList, err: err}
			}()

			var timer <-chan time.Time
			if timeout > 0 {
				t := time.NewTimer(timeout)
				defer t.Stop()
				timer = t.C
			}

			select {
			case o := <-ch:
				if o.err != nil {
					fmt.Fprintf(os.Stderr, "Warning: MCP server %q unavailable: %v\n", name, o.err)
					return
				}
				mu.Lock()
				result[name] = ConnectedServer{Client: o.client, Tools: o.tools}
				mu.Unlock()
			case <-timer:
				fmt.Fprintf(os.Stderr, "Warning: MCP server %q did not respond within %s, continuing without it\n", name, timeout)
				// Let the attempt finish in the background; if it turns out
				// the server was only slow (not hung), close the
				// now-unwanted connection as soon as it arrives instead of
				// leaving it open for the rest of the session.
				go func() {
					if o := <-ch; o.client != nil {
						o.client.Close()
					}
				}()
			}
		}()
	}
	wg.Wait()
	return result, nil
}

// connectOne creates, initializes, authenticates, and lists tools for a
// single configured server. It is the unbounded per-server body that
// ConnectAllTimeout races against a timer.
func connectOne(ctx context.Context, name string, serverCfg ServerConfig, authMgr *AuthManager) (Client, []Tool, error) {
	client, err := NewClient(name, serverCfg)
	if err != nil {
		return nil, nil, err
	}

	if err := client.Initialize(ctx); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("initialize: %w", err)
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

	toolList, err := client.ListTools(ctx)
	if err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("listing tools: %w", err)
	}

	return client, toolList, nil
}
