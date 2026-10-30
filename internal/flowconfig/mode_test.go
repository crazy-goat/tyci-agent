package flowconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModeConfig(t *testing.T, dir, text string) {
	t.Helper()
	path := filepath.Join(dir, ".tyci", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Exercise the public loader with the documented config, rather than constructing
// the new mode field directly. This also protects the strict shared-file parser.
func TestWorkflowModeConfigAndPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, project, want string
		trusted             bool
	}{
		{"global", `{}`, "semi", true},
		{"project auto", `{"workflows":{"issue-to-merge":{"mode":"auto"}}}`, "auto", true},
		{"omitted project mode", `{"workflows":{"issue-to-merge":{}}}`, "semi", true},
		{"untrusted project", `{"workflows":{"issue-to-merge":{"mode":"auto"}}}`, "semi", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, project := t.TempDir(), t.TempDir()
			writeModeConfig(t, home, `{"workflows":{"issue-to-merge":{"mode":"semi"}}}`)
			writeModeConfig(t, project, tc.project)
			cfg, err := Load(home, project, tc.trusted)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Workflows map[string]struct{ Mode string }
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got.Workflows["issue-to-merge"].Mode != tc.want {
				t.Fatalf("config = %s, want mode %s", data, tc.want)
			}
		})
	}
}

func TestWorkflowModeConfigRejectsUnknownValue(t *testing.T) {
	home, project := t.TempDir(), t.TempDir()
	writeModeConfig(t, home, `{"workflows":{"issue-to-merge":{"mode":"manual"}}}`)
	writeModeConfig(t, project, `{"workflows":{"issue-to-merge":{"mode":"semi"}}}`)
	if _, err := Load(home, project, true); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v", err)
	}
	writeModeConfig(t, home, `{}`)
	writeModeConfig(t, project, `{"workflows":{"issue-to-merge":{"mode":"manual"}}}`)
	if _, err := Load(home, project, true); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("error = %v", err)
	}
}
