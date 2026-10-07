package flowconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, base, rel, content string) string {
	t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_MissingFilesIsEmptyConfig(t *testing.T) {
	c, err := Load(t.TempDir(), t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Models) != 0 || len(c.Roles) != 0 {
		t.Fatalf("expected empty config, got %+v", c)
	}
}

func TestLoad_GlobalOnly(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u1"},"default_model":"a","roles":{"worker":{"prompt":"P"}},"check_timeout_sec":60}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Role("worker")
	if err != nil || r.Prompt != "P" {
		t.Fatalf("role: %+v %v", r, err)
	}
	if c.DefaultModel != "a" || c.CheckTimeoutSec != 60 {
		t.Fatalf("got %+v", c)
	}
}

func TestLoad_ProjectRoleReplacesGlobalRole(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"x":"u"},"roles":{"worker":{"model":"x","prompt":"A"}}}`)
	write(t, proj, ".tyci/config.json", `{"roles":{"worker":{"prompt":"B"}}}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := c.Role("worker")
	if r.Prompt != "B" || r.Model != "" {
		t.Fatalf("got %+v", r)
	}
}

func TestLoad_ModelsMergedPerAlias(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"g","b":"g2"},"default_model":"a"}`)
	write(t, proj, ".tyci/config.json", `{"models":{"a":"p","c":"p3"},"default_model":"c"}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Models["a"] != "p" || c.Models["b"] != "g2" || c.Models["c"] != "p3" || c.DefaultModel != "c" {
		t.Fatalf("got %+v", c)
	}
}

func TestLoad_UntrustedIgnoresProjectFile(t *testing.T) {
	home, proj := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".tyci", "config.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Load(home, proj, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Roles) != 0 {
		t.Fatalf("got %+v", c)
	}
	if _, err := Load(home, proj, true); err == nil {
		t.Fatal("trusted load should fail on a directory")
	}
}

func TestLoad_UnknownKeyIsError(t *testing.T) {
	home := t.TempDir()
	p := write(t, home, ".tyci/config.json", `{"rolez":{}}`)
	_, err := Load(home, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), p) {
		t.Fatalf("got %v", err)
	}
}

func TestLoad_AgentConfigKeysAreAccepted(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"favorite_models":["a/b"],"max_tokens":8000,"prompt_cache":false,"sidebar_visible":true,"auto_compact_percent":80,"default_model":"m","models":{"m":"x"}}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if c.DefaultModel != "m" {
		t.Fatalf("got %q", c.DefaultModel)
	}
}

func TestLoad_DirectProviderModelNamesAreAccepted(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"default_model":"nexos/GPT 5.6 Luna","roles":{"worker":{"model":"other/provider-model"}}}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.ResolveModel(Role{}); err != nil || got != "nexos/GPT 5.6 Luna" {
		t.Fatalf("default model: %q %v", got, err)
	}
	if got, err := c.ResolveModel(Role{Model: "other/provider-model"}); err != nil || got != "other/provider-model" {
		t.Fatalf("role model: %q %v", got, err)
	}
}

func TestLoad_RoleWithUnknownModelIsError(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u"},"roles":{"worker":{"model":"typo"}}}`)
	_, err := Load(home, t.TempDir(), true)
	if err == nil || !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("got %v", err)
	}
	write(t, home, ".tyci/config.json", `{"default_model":"nope"}`)
	if _, err := Load(home, t.TempDir(), true); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v", err)
	}
}

func TestLoad_PromptFileReference(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/w.md", "from file")
	write(t, home, ".tyci/config.json", `{"roles":{"worker":{"prompt":"@w.md"}}}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := c.Role("worker"); r.Prompt != "from file" {
		t.Fatalf("got %q", r.Prompt)
	}
}

func TestLoad_PromptFileRejected(t *testing.T) {
	cases := map[string]func(dir string){
		"@../x":       func(string) {},
		"@/abs/path":  func(string) {},
		"@missing.md": func(string) {},
		"@empty.md":   func(d string) { write(t, d, "empty.md", "") },
		"@link.md": func(d string) {
			write(t, d, "real.md", "x")
			if err := os.Symlink(filepath.Join(d, "real.md"), filepath.Join(d, "link.md")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for ref, setup := range cases {
		t.Run(ref, func(t *testing.T) {
			home := t.TempDir()
			setup(filepath.Join(home, ".tyci"))
			write(t, home, ".tyci/config.json", `{"roles":{"worker":{"prompt":"`+ref+`"}}}`)
			if _, err := Load(home, t.TempDir(), true); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRole_Undefined(t *testing.T) {
	c, _ := Load(t.TempDir(), t.TempDir(), true)
	if _, err := c.Role("planner"); err == nil || !strings.Contains(err.Error(), "planner") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveModel_FallsBackToDefault(t *testing.T) {
	c := &Config{Models: map[string]string{"a": "uri"}, DefaultModel: "a"}
	if u, err := c.ResolveModel(Role{}); err != nil || u != "uri" {
		t.Fatalf("got %q %v", u, err)
	}
}

func TestResolveModel_DirectProviderModelName(t *testing.T) {
	c := &Config{Models: map[string]string{}}
	if got, err := c.ResolveModel(Role{Model: "provider/model"}); err != nil || got != "provider/model" {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestResolveModel_UnknownAlias(t *testing.T) {
	c := &Config{Models: map[string]string{}}
	if _, err := c.ResolveModel(Role{Model: "zzz"}); err == nil || !strings.Contains(err.Error(), "zzz") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveModel_NoModelAnywhere(t *testing.T) {
	c := &Config{Models: map[string]string{}}
	if _, err := c.ResolveModel(Role{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckTimeout_Default(t *testing.T) {
	if d := (&Config{}).CheckTimeout(); d != 30*time.Minute {
		t.Fatal(d)
	}
	if d := (&Config{CheckTimeoutSec: 60}).CheckTimeout(); d != time.Minute {
		t.Fatal(d)
	}
}

func TestLoad_OrchestratorSectionIsAccepted(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"orchestrator":{"workers":2}}`)
	if _, err := Load(home, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
}
