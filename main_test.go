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
func (c *captureDisplay) Error(err error) {}
func (c *captureDisplay) End()            {}

func TestReplaySessionToDisplay(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/replay.jsonl"

	// Write a session file manually
	content := `{"type":"session","version":1,"id":"test","timestamp":"...","cwd":"/test","model":"mock","provider":"mock"}
{"type":"message","id":"m1","timestamp":"...","message":{"role":"assistant","content":[{"type":"thinking","thinking":"thinking text"},{"type":"text","text":"hello world"},{"type":"toolCall","id":"tc1","name":"bash","arguments":"{\"command\":\"ls\"}"}]},"usage":{"input":10,"output":5}}
{"type":"message","id":"m2","timestamp":"...","message":{"role":"toolResult","content":[{"type":"text","text":"file1\\nfile2","toolCallId":"tc1","toolName":"bash"}]}}
{"type":"session_end","id":"test","timestamp":"...","status":"ok","exit_code":0}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write session: %v", err)
	}

	c := newCapture()
	replaySessionToDisplay(c, path)

	if len(c.thinking) != 1 || c.thinking[0] != "thinking text" {
		t.Errorf("thinking: got %v, want ['thinking text']", c.thinking)
	}
	if len(c.text) != 1 || c.text[0] != "hello world" {
		t.Errorf("text: got %v, want ['hello world']", c.text)
	}
	if len(c.toolStarts) != 1 || c.toolStarts[0] != "bash" {
		t.Errorf("toolStarts: got %v, want ['bash']", c.toolStarts)
	}
	if len(c.toolDeltas) != 1 || c.toolDeltas[0] != `{"command":"ls"}` {
		t.Errorf("toolDeltas: got %v, want ['{\"command\":\"ls\"}']", c.toolDeltas)
	}
	if len(c.toolEnds) != 1 {
		t.Fatalf("toolEnds: got %v", c.toolEnds)
	}
	if !strings.Contains(c.toolEnds[0], "file1") || !strings.Contains(c.toolEnds[0], "file2") {
		t.Errorf("toolEnds[0] = %q, want containing 'file1' and 'file2'", c.toolEnds[0])
	}
	if len(c.summaries) != 1 || c.summaries[0].Input != 10 || c.summaries[0].Output != 5 {
		t.Errorf("summaries: got %v", c.summaries)
	}
}
