package flowconfig

import (
	"strings"
	"testing"
)

func TestRoleParseFull(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{
		"models":{"a":"u1","b":"u2","c":"u3"},
		"default_model":"a",
		"roles":{"review":{
			"model":"a","prompt":"P","effort":"high",
			"fallback":["b","c"],
			"permission":"read-only",
			"network":false,
			"tools":["read","find","mcp_x_*"],
			"limits":{"rounds":10,"time_min":5,"tool_calls":40,"ping_min":3}
		}}}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles["review"]
	if strings.Join(r.Fallback, ",") != "b,c" {
		t.Fatalf("fallback = %v", r.Fallback)
	}
	if r.PermissionMode() != PermissionReadOnly || r.NetworkOn() {
		t.Fatalf("permission/network = %q/%v", r.PermissionMode(), r.NetworkOn())
	}
	if strings.Join(r.Tools, ",") != "read,find,mcp_x_*" {
		t.Fatalf("tools = %v", r.Tools)
	}
	want := Limits{Rounds: 10, TimeMin: 5, ToolCalls: 40, PingMin: 3}
	if r.Limits != want {
		t.Fatalf("limits = %+v, want %+v", r.Limits, want)
	}
}

func TestRoleDefaults(t *testing.T) {
	home := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u1"},"roles":{"worker":{"model":"a","prompt":"P"}}}`)
	c, err := Load(home, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles["worker"]
	if r.PermissionMode() != PermissionWorktreeWrite {
		t.Fatalf("default permission = %q", r.PermissionMode())
	}
	if !r.NetworkOn() {
		t.Fatal("network must default to true")
	}
	if r.Tools != nil || r.Fallback != nil || r.Limits != (Limits{}) {
		t.Fatalf("expected empty tools, fallback and limits, got %+v", r)
	}
}

func TestRoleValidateErrors(t *testing.T) {
	cases := map[string]struct{ role, want string }{
		"unknown model":      {`{"model":"zz","prompt":"P"}`, `unknown model alias "zz"`},
		"unknown permission": {`{"prompt":"P","permission":"root"}`, `unknown permission "root"`},
		"negative limit":     {`{"prompt":"P","limits":{"rounds":-1}}`, `limits.rounds: -1 is negative`},
		"unknown fallback":   {`{"prompt":"P","fallback":["zz"]}`, `unknown fallback model alias "zz"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			write(t, home, ".tyci/config.json", `{"models":{"a":"u1"},"roles":{"worker":`+tc.role+`}}`)
			_, err := Load(home, t.TempDir(), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "worker") {
				t.Fatalf("err = %v, want it to name the role and %q", err, tc.want)
			}
		})
	}
}

func TestLocalOnlyRoleCappedAtWorktreeWrite(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u1"}}`)
	write(t, proj, ".tyci/config.json", `{"roles":{"fixer":{"prompt":"P","permission":"full"}}}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Roles["fixer"].PermissionMode(); got != PermissionWorktreeWrite {
		t.Fatalf("local-only full role has permission %q, want worktree-write", got)
	}
}

func TestLocalOnlyRoleMayBeReadOnly(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, proj, ".tyci/config.json", `{"roles":{"review":{"prompt":"P","permission":"read-only"}}}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Roles["review"].PermissionMode(); got != PermissionReadOnly {
		t.Fatalf("permission = %q", got)
	}
}

func TestLocalCannotWiden(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u1"},"roles":{"review":{"model":"a","prompt":"P","permission":"read-only","network":false,"tools":["read","find"]}}}`)
	write(t, proj, ".tyci/config.json", `{"roles":{"review":{"prompt":"Q","permission":"full","network":true,"tools":["read","bash","mcp_x_y"]}}}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles["review"]
	if r.PermissionMode() != PermissionReadOnly {
		t.Fatalf("permission widened to %q", r.PermissionMode())
	}
	if r.NetworkOn() {
		t.Fatal("network widened")
	}
	if strings.Join(r.Tools, ",") != "read" {
		t.Fatalf("tools = %v, want only read", r.Tools)
	}
	if r.Prompt != "Q" {
		t.Fatalf("local prompt not used: %q", r.Prompt)
	}
}

func TestLocalInheritsOmittedKeys(t *testing.T) {
	home := t.TempDir()
	proj := t.TempDir()
	write(t, home, ".tyci/config.json", `{"models":{"a":"u1"},"roles":{"review":{"model":"a","prompt":"P","permission":"read-only","network":false,"tools":["read"]}}}`)
	write(t, proj, ".tyci/config.json", `{"roles":{"review":{"prompt":"Q"}}}`)
	c, err := Load(home, proj, true)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Roles["review"]
	if r.PermissionMode() != PermissionReadOnly || r.NetworkOn() || strings.Join(r.Tools, ",") != "read" {
		t.Fatalf("omitted keys not inherited: %+v", r)
	}
}

func TestLocalToolsCannotAddMCPWhenGlobalHasNone(t *testing.T) {
	got := tighter(Role{}, Role{Tools: []string{"read", "mcp_x_y", "mcp_z_*"}})
	if strings.Join(got.Tools, ",") != "read" {
		t.Fatalf("tools = %v", got.Tools)
	}
}

func TestLocalToolsMayUseGlobalWildcard(t *testing.T) {
	got := tighter(Role{Tools: []string{"mcp_x_*"}}, Role{Tools: []string{"mcp_x_y", "mcp_q_y"}})
	if strings.Join(got.Tools, ",") != "mcp_x_y" {
		t.Fatalf("tools = %v", got.Tools)
	}
}

func TestResolveFallbacks(t *testing.T) {
	c := &Config{Models: map[string]string{"fast": "openai/gpt-fast"}}
	got, err := c.ResolveFallbacks(Role{Fallback: []string{"fast", "anthropic/claude-y"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "openai/gpt-fast,anthropic/claude-y" {
		t.Fatalf("got %v", got)
	}
	if got, err := c.ResolveFallbacks(Role{}); err != nil || got != nil {
		t.Fatalf("no fallbacks: got %v err %v", got, err)
	}
	if _, err := c.ResolveFallbacks(Role{Fallback: []string{"nope"}}); err == nil {
		t.Fatal("unknown alias accepted")
	}
}
