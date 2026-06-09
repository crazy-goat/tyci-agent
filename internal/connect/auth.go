package connect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AuthPath returns the path to the auth.json file.
func AuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			home = "/tmp"
		}
	}
	return filepath.Join(home, ".tyci", "auth.json")
}

// LoadAuth reads the auth.json file and returns a map of provider -> API key.
// Returns nil if the file does not exist.
func LoadAuth() (map[string]string, error) {
	path := AuthPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading auth file: %w", err)
	}
	var auth map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, fmt.Errorf("parsing auth file: %w", err)
	}
	return auth, nil
}

// SaveAuth writes the auth map to auth.json with permissions 0600.
func SaveAuth(auth map[string]string) error {
	if auth == nil {
		auth = make(map[string]string)
	}
	path := AuthPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding auth file: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing auth file: %w", err)
	}
	return nil
}

// SetKey saves an API key for the given provider in auth.json.
// Returns an error if key is empty.
func SetKey(provider, key string) error {
	if key == "" {
		return fmt.Errorf("API key cannot be empty")
	}
	auth, err := LoadAuth()
	if err != nil {
		return err
	}
	if auth == nil {
		auth = make(map[string]string)
	}
	auth[provider] = key
	return SaveAuth(auth)
}

// GetKey retrieves the API key for the given provider from auth.json.
// Returns the key, whether it was found, and any error.
func GetKey(provider string) (string, bool, error) {
	auth, err := LoadAuth()
	if err != nil {
		return "", false, err
	}
	if auth == nil {
		return "", false, nil
	}
	key, ok := auth[provider]
	if !ok {
		return "", false, nil
	}
	return key, true, nil
}

// RemoveKey deletes the API key for the given provider from auth.json.
func RemoveKey(provider string) error {
	auth, err := LoadAuth()
	if err != nil {
		return err
	}
	if auth == nil {
		return nil
	}
	delete(auth, provider)
	return SaveAuth(auth)
}

// ListKeys returns a list of provider names that have keys in auth.json.
func ListKeys() ([]string, error) {
	auth, err := LoadAuth()
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, nil
	}
	keys := make([]string, 0, len(auth))
	for p := range auth {
		keys = append(keys, p)
	}
	return keys, nil
}

// MaskKey returns a masked version of the key showing only the last 4 characters.
func MaskKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return strings.Repeat("*", len(key)-4) + key[len(key)-4:]
}
