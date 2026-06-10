package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthManagerTokenStorage(t *testing.T) {
	am := NewAuthManager()

	// Set and get token
	am.SetToken("test-server", "test-token-123")

	token, ok := am.GetToken("test-server")
	if !ok || token != "test-token-123" {
		t.Errorf("expected test-token-123, got %s (found=%v)", token, ok)
	}

	// Non-existent server
	_, ok = am.GetToken("nonexistent")
	if ok {
		t.Error("expected not found for nonexistent server")
	}

	// Remove token
	am.RemoveToken("test-server")
	_, ok = am.GetToken("test-server")
	if ok {
		t.Error("expected token to be removed")
	}
}

func TestAuthManagerExpiry(t *testing.T) {
	am := NewAuthManager()

	// Token that expires in the past
	am.SetTokenWithExpiry("expired-server", "expired-token", time.Now().Add(-1*time.Hour))

	_, ok := am.GetToken("expired-server")
	if ok {
		t.Error("expected expired token to be removed")
	}

	// Token that expires in the future
	am.SetTokenWithExpiry("valid-server", "valid-token", time.Now().Add(1*time.Hour))

	token, ok := am.GetToken("valid-server")
	if !ok || token != "valid-token" {
		t.Errorf("expected valid-token, got %s", token)
	}
}

func TestAuthManagerSaveLoad(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "mcp-auth-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create auth manager with custom path
	am := &AuthManager{
		tokens:    make(map[string]*TokenEntry),
		tokenFile: filepath.Join(tmpDir, "mcp_auth.json"),
	}

	// Set some tokens
	am.SetToken("server1", "token1")
	am.SetToken("server2", "token2")

	// Save
	if err := am.Save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load into new manager
	am2 := &AuthManager{
		tokens:    make(map[string]*TokenEntry),
		tokenFile: filepath.Join(tmpDir, "mcp_auth.json"),
	}

	if err := am2.Load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Verify tokens
	token1, ok := am2.GetToken("server1")
	if !ok || token1 != "token1" {
		t.Errorf("expected token1, got %s", token1)
	}

	token2, ok := am2.GetToken("server2")
	if !ok || token2 != "token2" {
		t.Errorf("expected token2, got %s", token2)
	}
}

func TestGetTokenForServer(t *testing.T) {
	am := NewAuthManager()
	am.SetToken("stored-server", "stored-token")

	tests := []struct {
		name      string
		server    string
		authCfg   *AuthConfig
		wantToken string
	}{
		{
			name:      "nil auth config",
			server:    "any",
			authCfg:   nil,
			wantToken: "",
		},
		{
			name:      "none type",
			server:    "any",
			authCfg:   &AuthConfig{Type: "none"},
			wantToken: "",
		},
		{
			name:      "stored token",
			server:    "stored-server",
			authCfg:   &AuthConfig{Type: "bearer"},
			wantToken: "stored-token",
		},
		{
			name:      "env var",
			server:    "env-server",
			authCfg:   &AuthConfig{Type: "bearer", TokenEnv: "TEST_MCP_TOKEN"},
			wantToken: "env-token",
		},
		{
			name:      "literal token",
			server:    "literal-server",
			authCfg:   &AuthConfig{Type: "bearer", Token: "literal-token"},
			wantToken: "literal-token",
		},
	}

	// Set env var for test
	os.Setenv("TEST_MCP_TOKEN", "env-token")
	defer os.Unsetenv("TEST_MCP_TOKEN")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := GetTokenForServer(am, tt.server, tt.authCfg)
			if token != tt.wantToken {
				t.Errorf("expected %q, got %q", tt.wantToken, token)
			}
		})
	}
}

func TestAuthManagerListServers(t *testing.T) {
	am := NewAuthManager()
	am.SetToken("server1", "token1")
	am.SetToken("server2", "token2")
	am.SetToken("server3", "token3")

	servers := am.ListServers()
	if len(servers) != 3 {
		t.Errorf("expected 3 servers, got %d", len(servers))
	}
}

func TestAuthManagerIsExpired(t *testing.T) {
	am := NewAuthManager()

	// Non-existent server is expired
	if !am.IsExpired("nonexistent") {
		t.Error("expected nonexistent server to be expired")
	}

	// Token without expiry is not expired
	am.SetToken("no-expiry", "token")
	if am.IsExpired("no-expiry") {
		t.Error("expected no-expiry token to not be expired")
	}

	// Expired token
	am.SetTokenWithExpiry("expired", "token", time.Now().Add(-1*time.Hour))
	if !am.IsExpired("expired") {
		t.Error("expected expired token to be expired")
	}

	// Valid token
	am.SetTokenWithExpiry("valid", "token", time.Now().Add(1*time.Hour))
	if am.IsExpired("valid") {
		t.Error("expected valid token to not be expired")
	}
}
