package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/decodo/tyci/stream"
)

var (
	binPath string
	testDir string
)

// testEnv returns an environment slice with HOME set to testDir
// so the subprocess finds the test model.json.
func testEnv(extra ...string) []string {
	env := os.Environ()
	found := false
	for i, e := range env {
		if strings.HasPrefix(e, "HOME=") {
			env[i] = "HOME=" + testDir
			found = true
			break
		}
	}
	if !found {
		env = append(env, "HOME="+testDir)
	}
	return append(env, extra...)
}

func TestMain(m *testing.M) {
	var err error
	testDir, err = os.MkdirTemp("", "tyci-test")
	if err != nil {
		os.Stderr.WriteString("mkdir temp: " + err.Error())
		os.Exit(1)
	}
	// Remove the temp directory in a defer; ignore errors on cleanup.
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create a minimal model.json so subprocess tests can find a model.
	tyciDir := filepath.Join(testDir, ".tyci")
	if err := os.MkdirAll(tyciDir, 0755); err != nil {
		_, _ = os.Stderr.WriteString("mkdir .tyci: " + err.Error())
		os.Exit(1)
	}
	modelCfg := map[string]map[string]map[string]string{
		"test-provider": {
			"test-model": {
				"uri": "openai://test-model@$TEST_API_KEY@example.com/v1",
			},
		},
	}
	data, _ := json.Marshal(modelCfg)
	if err := os.WriteFile(filepath.Join(tyciDir, "model.json"), data, 0644); err != nil {
		_, _ = os.Stderr.WriteString("write model.json: " + err.Error())
		os.Exit(1)
	}

	// Stub providers.json (models.dev catalog) so EnsureProvidersJSON skips
	// the network download during tests.
	if err := os.WriteFile(filepath.Join(tyciDir, "providers.json"), []byte("{}"), 0644); err != nil {
		_, _ = os.Stderr.WriteString("write providers.json: " + err.Error())
		os.Exit(1)
	}

	// Create a default agent so ResolveModel finds a model without --model flag.
	agentCfg := map[string]map[string]any{
		"default": {
			"model": "test-provider/test-model",
		},
	}
	data2, _ := json.Marshal(agentCfg)
	if err := os.WriteFile(filepath.Join(tyciDir, "agents.json"), data2, 0644); err != nil {
		_, _ = os.Stderr.WriteString("write agents.json: " + err.Error())
		os.Exit(1)
	}

	binPath = filepath.Join(testDir, "tyci")
	out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput()
	if err != nil {
		os.Stderr.WriteString("build failed: " + string(out))
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestRunRequiresPrompt(t *testing.T) {
	cmd := exec.Command(binPath, "run", "--prompt", "")
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected error for `run` without --prompt")
	}
	if !strings.Contains(string(out), "--prompt is required") {
		t.Errorf("output should mention 'requires --prompt', got: %s", string(out))
	}
}

func TestNoPromptFlagExitsZero(t *testing.T) {
	cmd := exec.Command(binPath)
	cmd.Env = testEnv()
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("exit code %d, stderr: %s", exitErr.ExitCode(), exitErr.Stderr)
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHelpExitsZero(t *testing.T) {
	cmd := exec.Command(binPath, "--help")
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("exit code %d:\n%s", exitErr.ExitCode(), string(out))
		}
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) == 0 {
		t.Error("--help produced no output")
	}
}

func TestInteractiveModelNotExistError(t *testing.T) {
	// Non-existent model should print error and exit
	cmd := exec.Command(binPath, "console", "--model", "nonexistent/model")
	cmd.Env = testEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected error for non-existent model")
	}
	if !strings.Contains(string(out), "not found") {
		t.Errorf("output should contain 'not found', got: %s", string(out))
	}
}

// captureDisplay implements display.Display for testing replay.
type captureDisplay struct {
	mu         sync.Mutex
	thinking   []string
	text       []string
	toolStarts []string
	toolDeltas []string
	toolEnds   []string
	toolBlocks []string
	summaries  []stream.Usage
}

func newCapture() *captureDisplay { return &captureDisplay{} }

func (c *captureDisplay) Request(string) {}
func (c *captureDisplay) Thinking(text string) {
	c.mu.Lock()
	c.thinking = append(c.thinking, text)
	c.mu.Unlock()
}
func (c *captureDisplay) Text(text string) { c.mu.Lock(); c.text = append(c.text, text); c.mu.Unlock() }
func (c *captureDisplay) ToolCallStart(name string) {
	c.mu.Lock()
	c.toolStarts = append(c.toolStarts, name)
	c.mu.Unlock()
}
func (c *captureDisplay) ToolCallDelta(delta string) {
	c.mu.Lock()
	c.toolDeltas = append(c.toolDeltas, delta)
	c.mu.Unlock()
}
func (c *captureDisplay) ToolCallEnd(name, result string) {
	c.mu.Lock()
	c.toolEnds = append(c.toolEnds, name+":"+result)
	c.mu.Unlock()
}
func (c *captureDisplay) ToolFinish() {}
func (c *captureDisplay) ToolBlock(msg string) {
	c.mu.Lock()
	c.toolBlocks = append(c.toolBlocks, msg)
	c.mu.Unlock()
}
func (c *captureDisplay) Summary(usage stream.Usage, stats stream.Stats) {
	c.mu.Lock()
	c.summaries = append(c.summaries, usage)
	c.mu.Unlock()
}
func (c *captureDisplay) Total(usage stream.Usage) {}
func (c *captureDisplay) Error(err error)          {}
func (c *captureDisplay) End()                     {}

// TestReplaySessionToDisplay verifies the long-session-safe replay path:
// every event in the JSONL hits the display ONLY through ToolBlock (so
// glamour / streaming wrappers never run on replay), the dropped per-
// event routes (Thinking/Text/ToolCallStart/...) stay silent, and
// selected spans resolve to concrete characters on screen.
//
// The legacy test in this slot asserted the opposite: it expected every
// Thinking/Text/ToolCallStart/End call. That code path produced the
// "screen blanks on selection release" and "PgUp/PgDown loses the scroll
// anchor" bugs on long sessions — see the docstring on
// replaySessionToDisplay for the full write-up.
func TestReplaySessionToDisplay(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/replay.jsonl"

	content := `{"type":"session","version":1,"id":"test","timestamp":"...","cwd":"/test","model":"mock","provider":"mock"}
{"type":"message","id":"m1","timestamp":"...","message":{"role":"assistant","content":[{"type":"thinking","thinking":"thinking text"},{"type":"text","text":"hello world"},{"type":"toolCall","id":"tc1","name":"bash","arguments":"{\"command\":\"ls\"}"}]},"usage":{"input":10,"output":5}}
{"type":"message","id":"m2","timestamp":"...","message":{"role":"toolResult","content":[{"type":"text","text":"file1\nfile2","toolCallId":"tc1","toolName":"bash"}]}}
{"type":"session_end","id":"test","timestamp":"...","status":"ok","exit_code":0}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	c := newCapture()
	replaySessionToDisplay(c, path)

	// Per-event paths must NOT fire — those raced with selection / scroll.
	if len(c.thinking) != 0 {
		t.Errorf("thinking calls should be elided on replay, got %v", c.thinking)
	}
	if len(c.text) != 0 {
		t.Errorf("text calls should be elided on replay, got %v", c.text)
	}
	if len(c.toolStarts) != 0 {
		t.Errorf("toolStart calls should be elided on replay, got %v", c.toolStarts)
	}
	if len(c.toolDeltas) != 0 {
		t.Errorf("toolDelta calls should be elided on replay, got %v", c.toolDeltas)
	}
	if len(c.toolEnds) != 0 {
		t.Errorf("toolEnd calls should be elided on replay, got %v", c.toolEnds)
	}
	if len(c.summaries) != 0 {
		t.Errorf("summary calls should be elided on replay, got %v", c.summaries)
	}

	// Two real messages + the trailing marker → at least 3 ToolBlocks.
	if len(c.toolBlocks) < 3 {
		t.Fatalf("expected at least 3 ToolBlocks (assistant, toolResult, continuation), got %d", len(c.toolBlocks))
	}

	first := c.toolBlocks[0]
	if !strings.Contains(first, "[Assistant thinking: 13 chars / 1 lines — collapsed]") {
		t.Errorf("first block should contain collapsed thinking summary, got:\n%s", first)
	}
	if !strings.Contains(first, "[Assistant]\nhello world") {
		t.Errorf("first block should contain assistant text, got:\n%s", first)
	}
	if !strings.Contains(first, "[Assistant tool calls]") {
		t.Errorf("first block should contain the tool call summary, got:\n%s", first)
	}

	second := c.toolBlocks[1]
	if !strings.Contains(second, "[Tool result: bash]") {
		t.Errorf("second block should be the bash tool result, got:\n%s", second)
	}
	if !strings.Contains(second, "file1") || !strings.Contains(second, "file2") {
		t.Errorf("tool result body should contain stdout, got:\n%s", second)
	}

	last := c.toolBlocks[len(c.toolBlocks)-1]
	if !strings.Contains(last, "Continuing from session end") {
		t.Errorf("last block must be the continuation marker, got:\n%s", last)
	}
}
