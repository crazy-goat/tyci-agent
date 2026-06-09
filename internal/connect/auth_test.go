package connect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tyci-auth-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// setHome temporarily sets HOME for the test and restores it.
func setHome(t *testing.T, dir string) {
	t.Helper()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
}

func TestAuthPath(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	path := AuthPath()
	want := filepath.Join(dir, ".tyci", "auth.json")
	if path != want {
		t.Errorf("AuthPath() = %q, want %q", path, want)
	}
}

func TestSaveAndLoadAuth(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	auth := map[string]string{
		"openai":    "sk-test-123",
		"anthropic": "sk-ant-test-456",
	}

	if err := SaveAuth(auth); err != nil {
		t.Fatalf("SaveAuth() error: %v", err)
	}

	// Verify file exists with correct permissions
	info, err := os.Stat(AuthPath())
	if err != nil {
		t.Fatalf("stat auth file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("auth file permissions = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error: %v", err)
	}
	if len(loaded) != 2 {
		t.Errorf("LoadAuth() returned %d keys, want 2", len(loaded))
	}
	if loaded["openai"] != "sk-test-123" {
		t.Errorf("LoadAuth() openai key = %q, want %q", loaded["openai"], "sk-test-123")
	}
	if loaded["anthropic"] != "sk-ant-test-456" {
		t.Errorf("LoadAuth() anthropic key = %q, want %q", loaded["anthropic"], "sk-ant-test-456")
	}
}

func TestLoadAuthNotExists(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	loaded, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error: %v", err)
	}
	if loaded != nil {
		t.Errorf("LoadAuth() = %v, want nil for non-existent file", loaded)
	}
}

func TestLoadAuthCorrupted(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	// Write invalid JSON
	os.MkdirAll(filepath.Join(dir, ".tyci"), 0755)
	os.WriteFile(AuthPath(), []byte("not json"), 0600)

	_, err := LoadAuth()
	if err == nil {
		t.Error("LoadAuth() expected error for corrupted file")
	}
}

func TestSetKey(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	if err := SetKey("test-provider", "test-key-789"); err != nil {
		t.Fatalf("SetKey() error: %v", err)
	}

	key, ok, err := GetKey("test-provider")
	if err != nil {
		t.Fatalf("GetKey() error: %v", err)
	}
	if !ok {
		t.Error("GetKey() returned ok=false, want true")
	}
	if key != "test-key-789" {
		t.Errorf("GetKey() = %q, want %q", key, "test-key-789")
	}
}

func TestSetKeyEmpty(t *testing.T) {
	if err := SetKey("test", ""); err == nil {
		t.Error("SetKey() expected error for empty key")
	}
}

func TestSetKeyOverwrite(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	SetKey("provider1", "key1")
	SetKey("provider1", "key2")

	key, ok, _ := GetKey("provider1")
	if !ok {
		t.Error("GetKey() returned ok=false after overwrite")
	}
	if key != "key2" {
		t.Errorf("GetKey() after overwrite = %q, want %q", key, "key2")
	}
}

func TestGetKeyNotFound(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	key, ok, err := GetKey("nonexistent")
	if err != nil {
		t.Fatalf("GetKey() error: %v", err)
	}
	if ok {
		t.Error("GetKey() returned ok=true for nonexistent provider")
	}
	if key != "" {
		t.Errorf("GetKey() = %q, want empty string", key)
	}
}

func TestRemoveKey(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	SetKey("provider1", "key1")
	SetKey("provider2", "key2")

	if err := RemoveKey("provider1"); err != nil {
		t.Fatalf("RemoveKey() error: %v", err)
	}

	// provider1 should be gone
	_, ok, _ := GetKey("provider1")
	if ok {
		t.Error("provider1 key should be removed")
	}

	// provider2 should remain
	_, ok, _ = GetKey("provider2")
	if !ok {
		t.Error("provider2 key should still exist")
	}
}

func TestRemoveKeyNonExistent(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	// Should not error when removing non-existent key
	if err := RemoveKey("nonexistent"); err != nil {
		t.Errorf("RemoveKey() on non-existent key: %v", err)
	}
}

func TestRemoveKeyEmptyFile(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	// Should not error when auth.json doesn't exist
	if err := RemoveKey("test"); err != nil {
		t.Errorf("RemoveKey() with no auth file: %v", err)
	}
}

func TestListKeys(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	SetKey("provider1", "key1")
	SetKey("provider2", "key2")

	keys, err := ListKeys()
	if err != nil {
		t.Fatalf("ListKeys() error: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("ListKeys() returned %d keys, want 2", len(keys))
	}

	// Should contain both providers
	seen := make(map[string]bool)
	for _, k := range keys {
		seen[k] = true
	}
	if !seen["provider1"] {
		t.Error("ListKeys() missing provider1")
	}
	if !seen["provider2"] {
		t.Error("ListKeys() missing provider2")
	}
}

func TestListKeysEmpty(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	keys, err := ListKeys()
	if err != nil {
		t.Fatalf("ListKeys() error: %v", err)
	}
	if keys != nil {
		t.Errorf("ListKeys() = %v, want nil for empty auth", keys)
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "****"},
		{"a", "****"},
		{"abcd", "****"},
		{"abcde", "*bcde"},
		{"abcdef", "**cdef"},
		{"sk-test-abc", "*******-abc"},
		{strings.Repeat("x", 50), strings.Repeat("*", 46) + "xxxx"},
	}

	for _, tt := range tests {
		got := MaskKey(tt.input)
		if got != tt.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tt.input, got, tt.want)
		}
		// Last 4 chars should always match if len > 4
		if len(tt.input) > 4 {
			if got[len(got)-4:] != tt.input[len(tt.input)-4:] {
				t.Errorf("MaskKey(%q) last 4 chars mismatch: %q vs %q", tt.input, got[len(got)-4:], tt.input[len(tt.input)-4:])
			}
		}
	}
}

func TestSaveAuthNil(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	// SaveAuth with nil should create empty file
	if err := SaveAuth(nil); err != nil {
		t.Fatalf("SaveAuth(nil) error: %v", err)
	}

	loaded, err := LoadAuth()
	if err != nil {
		t.Fatalf("LoadAuth() error: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("LoadAuth() after SaveAuth(nil) returned %d keys, want 0", len(loaded))
	}
}

func TestRoundTripWithSpecialChars(t *testing.T) {
	dir := tempDir(t)
	setHome(t, dir)

	specialKey := "sk-test-with-$pecial_chars_and_dots.v1+more"
	if err := SetKey("special-provider", specialKey); err != nil {
		t.Fatalf("SetKey() error: %v", err)
	}

	key, ok, err := GetKey("special-provider")
	if err != nil {
		t.Fatalf("GetKey() error: %v", err)
	}
	if !ok {
		t.Error("GetKey() returned ok=false")
	}
	if key != specialKey {
		t.Errorf("GetKey() = %q, want %q", key, specialKey)
	}
}
