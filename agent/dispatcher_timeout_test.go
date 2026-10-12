package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/tools"
)

// writeToolsConfig writes a config.json with body to dir/.tyci/config.json
// and returns dir. The test HOME must already be isolated.
func writeToolsConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, GlobalConfigDir, localConfigName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeGlobalToolsConfig writes the global ~/.tyci/config.json under HOME.
func writeGlobalToolsConfig(t *testing.T, body string) {
	t.Helper()
	writeToolsConfig(t, os.Getenv("HOME"), body)
}

func TestTimeoutDefaults(t *testing.T) {
	isolateHome(t)
	cases := map[string]time.Duration{
		"read":    2500 * time.Millisecond,
		"find":    2500 * time.Millisecond,
		"write":   2500 * time.Millisecond,
		"bash":    60 * time.Second,
		"web":     60 * time.Second,
		"lua":     60 * time.Second,
		"unknown": 60 * time.Second,
	}
	for name, want := range cases {
		if got := timeoutFor(ToolsConfig{}, name); got != want {
			t.Errorf("timeoutFor(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestTimeoutMCPIsSlow(t *testing.T) {
	isolateHome(t)
	runner := tools.NewMCPToolRunner()
	runner.AddToolForTests("mcp_srv_search")
	restore := tools.SetMCPToolRunnerForTests(runner)
	t.Cleanup(restore)

	fast := 1.0
	cfg := ToolsConfig{BackgroundAfterFastSec: &fast}
	if got := timeoutFor(cfg, "mcp_srv_search"); got != 60*time.Second {
		t.Errorf("MCP tool timeout = %v, want 60s (slow class)", got)
	}
}

func TestTimeoutOverridePerTool(t *testing.T) {
	isolateHome(t)
	cfg := ToolsConfig{BackgroundAfterSec: map[string]float64{"read": 10}}
	if got := timeoutFor(cfg, "read"); got != 10*time.Second {
		t.Errorf("read with override = %v, want 10s", got)
	}
	if got := timeoutFor(cfg, "find"); got != 2500*time.Millisecond {
		t.Errorf("find without override = %v, want the fast default 2.5s", got)
	}
	if got := timeoutFor(cfg, "bash"); got != 60*time.Second {
		t.Errorf("bash without override = %v, want the slow default 60s", got)
	}
}

func TestTimeoutZeroMeansImmediate(t *testing.T) {
	isolateHome(t)
	zero := 0.0
	cfg := ToolsConfig{BackgroundAfterFastSec: &zero}
	if got := timeoutFor(cfg, "read"); got != 0 {
		t.Fatalf("read with fast=0 = %v, want 0", got)
	}

	b := bus.New()
	d := NewDispatcher(b, testSelf, cfg)
	t.Cleanup(d.Close)
	id := d.Fire(context.Background(), func(c context.Context) (string, bool) {
		<-c.Done()
		return "", true
	})
	out := d.Await(context.Background(), id, timeoutFor(cfg, "read"))
	if !out.Background || !strings.HasPrefix(out.Text, placeholderPrefix) {
		t.Errorf("outcome = %+v, want a placeholder at once", out)
	}
}

func TestTimeoutLocalOverridesGlobal(t *testing.T) {
	isolateHome(t)
	writeGlobalToolsConfig(t, `{"tools": {"background_after_fast_sec": 10, "background_after_slow_sec": 90}}`)
	wd := writeToolsConfig(t, t.TempDir(), `{"tools": {"background_after_fast_sec": 1}}`)

	cfg := LoadTyciConfigFrom(wd).ToolsSettings()
	if got := timeoutFor(cfg, "read"); got != time.Second {
		t.Errorf("read with local fast=1 = %v, want 1s", got)
	}
	if got := timeoutFor(cfg, "bash"); got != 90*time.Second {
		t.Errorf("bash with global slow=90 = %v, want 90s (local file does not set it)", got)
	}
}

func TestTimeoutLocalZeroOverridesGlobal(t *testing.T) {
	isolateHome(t)
	writeGlobalToolsConfig(t, `{"tools": {"background_after_fast_sec": 10}}`)
	wd := writeToolsConfig(t, t.TempDir(), `{"tools": {"background_after_fast_sec": 0}}`)

	cfg := LoadTyciConfigFrom(wd).ToolsSettings()
	if got := timeoutFor(cfg, "read"); got != 0 {
		t.Errorf("read with local fast=0 over global 10 = %v, want 0", got)
	}
}

func TestTimeoutLocalMapMergesPerKey(t *testing.T) {
	isolateHome(t)
	writeGlobalToolsConfig(t, `{"tools": {"background_after_sec": {"bash": 10, "web": 20}}}`)
	wd := writeToolsConfig(t, t.TempDir(), `{"tools": {"background_after_sec": {"bash": 0}}}`)

	cfg := LoadTyciConfigFrom(wd).ToolsSettings()
	if got := timeoutFor(cfg, "bash"); got != 0 {
		t.Errorf("bash = %v, want the local 0", got)
	}
	if got := timeoutFor(cfg, "web"); got != 20*time.Second {
		t.Errorf("web = %v, want the global 20s", got)
	}
}

func TestTimeoutNegativeIsError(t *testing.T) {
	cases := []struct {
		name string
		body string
		key  string
	}{
		{"fast", `{"tools": {"background_after_fast_sec": -1}}`, "tools.background_after_fast_sec"},
		{"slow", `{"tools": {"background_after_slow_sec": -0.5}}`, "tools.background_after_slow_sec"},
		{"override", `{"tools": {"background_after_sec": {"bash": -1}}}`, "tools.background_after_sec.bash"},
		{"subagent", `{"tools": {"background_after_sec": {"subagent": 5}}}`, "tools.background_after_sec.subagent"},
		{"tail", `{"tools": {"result_tail_bytes": -1}}`, "tools.result_tail_bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateHome(t)
			writeGlobalToolsConfig(t, tc.body)
			err := LoadTyciConfigFrom("").ToolsSettings().Validate()
			if err == nil {
				t.Fatalf("Validate(%s) = nil, want an error", tc.body)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name key %q", err, tc.key)
			}
		})
	}
}

func TestResultTailBytesParsed(t *testing.T) {
	isolateHome(t)
	cases := map[string]int{
		`{}`:                                     DefaultResultTailBytes,
		`{"tools": {}}`:                          DefaultResultTailBytes,
		`{"tools": {"result_tail_bytes": 4096}}`: 4096,
		`{"tools": {"result_tail_bytes": 0}}`:    0,
	}
	for body, want := range cases {
		writeGlobalToolsConfig(t, body)
		cfg := LoadTyciConfigFrom("").ToolsSettings()
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: Validate = %v", body, err)
		}
		if got := cfg.ResultTailLimit(); got != want {
			t.Errorf("%s: ResultTailLimit = %d, want %d", body, got, want)
		}
	}
}

func TestNeverInlineIgnoreClass(t *testing.T) {
	isolateHome(t)
	zero := 0.0
	cfg := ToolsConfig{
		BackgroundAfterFastSec: &zero,
		BackgroundAfterSlowSec: &zero,
		BackgroundAfterSec: map[string]float64{
			"subagent":   0,
			"ask_parent": 0,
			"wait":       0,
		},
	}
	for _, name := range []string{"subagent", "ask_parent", "wait"} {
		if got := timeoutFor(cfg, name); got != noDeadline {
			t.Errorf("timeoutFor(%q) = %v, want noDeadline", name, got)
		}
	}
}
