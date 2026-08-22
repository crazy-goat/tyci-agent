// Package hooks runs user-supplied shell commands around tool calls.
//
// The point is leverage: one small mechanism buys autoformatting, lint
// feedback, protected paths, notifications and audit logging without any of
// them being built into tyci. A hook is a plain command, so it can be written
// in whatever the user already knows.
//
// Two events:
//
//   - pre_tool runs before the tool. A non-zero exit BLOCKS the call, and
//     what the hook printed becomes the error the model sees. This is how a
//     path gets protected, or a command vetoed.
//   - post_tool runs after the tool. What it prints is appended to the tool
//     result as a labelled note, so the model reads it in the same turn it
//     made the change — that is what makes "gofmt after every write" close
//     the loop instead of surfacing three minutes later in a build. With
//     "blocking": true a non-zero exit also marks the result failed.
//
// A hook is selected by event, by tool name, and optionally by the path the
// call is about (Hook.Paths) — the last of which is what lets one config file
// hold a Go formatter and a PHP linter without either running on the other's
// files.
//
// This package deliberately takes and returns plain types (strings, maps,
// bools) rather than tyci's ToolResult: it is imported by the tools package,
// so it must not import it back.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

const (
	// EventPreTool fires before a tool runs and can veto it.
	EventPreTool = "pre_tool"
	// EventPostTool fires after a tool ran and can annotate its result.
	EventPostTool = "post_tool"

	// defaultTimeoutSec bounds a single hook. Hooks run on the critical path
	// of every matching tool call, so the default is short on purpose: a
	// formatter or a linter on one file fits easily, and anything slower
	// should say so explicitly in its config.
	defaultTimeoutSec = 10

	// maxHookOutput caps what one hook may contribute. A hook that dumps a
	// whole build log would otherwise push out the context the model needs
	// for the actual task.
	maxHookOutput = 8 * 1024
)

// Hook is one configured command.
type Hook struct {
	Event string `json:"event"`
	// Tools restricts the hook to these tool names. Empty or ["*"] means
	// every tool. Names are matched exactly — no globbing, because a typo in
	// a glob silently matches nothing and a silently inactive hook is worse
	// than a config error.
	Tools []string `json:"tools"`
	// Command is run through the shell, so pipes and && work as written.
	Command string `json:"command"`
	// Timeout in seconds; 0 uses defaultTimeoutSec.
	Timeout int `json:"timeout"`
	// Blocking makes a non-zero exit from a post_tool hook mark the tool
	// result as failed. Ignored for pre_tool, which always blocks on a
	// non-zero exit — vetoing is its whole purpose.
	Blocking bool `json:"blocking"`
	// Paths restricts the hook to tool calls whose "path" argument matches one
	// of these globs. Empty means every path — today's behaviour.
	//
	// This is what makes one config file work for a mixed repository: a
	// formatter for "**/*.go" and a linter for "**/*.php" can both be
	// configured on the write tool without either one running on the other's
	// files. Without it a hook would have to re-implement the filter in shell,
	// and a hook that quietly matches nothing is worse than a config error.
	//
	// Matched with doublestar, so "**" crosses directories and "*" does not:
	// "**/*.go" is almost always what is meant, "*.go" only matches files in
	// the working directory itself. A tool call with no path argument (bash,
	// find) never matches a path-restricted hook — such a hook is about files
	// by construction.
	Paths []string `json:"paths"`
	// Name is optional and only used in messages, so the model and the user
	// can tell which hook spoke.
	Name string `json:"name"`
}

func (h Hook) label() string {
	if h.Name != "" {
		return h.Name
	}
	return firstWords(h.Command, 40)
}

func (h Hook) matches(tool, path string) bool {
	return h.matchesTool(tool) && h.matchesPath(path)
}

func (h Hook) matchesTool(tool string) bool {
	if len(h.Tools) == 0 {
		return true
	}
	for _, t := range h.Tools {
		if t == "*" || t == tool {
			return true
		}
	}
	return false
}

// matchesPath applies the Paths filter. An empty filter matches anything,
// including a tool call that has no path at all; a non-empty one requires a
// path and requires it to match.
func (h Hook) matchesPath(path string) bool {
	if len(h.Paths) == 0 {
		return true
	}
	if path == "" {
		return false
	}
	// Compared in slash form so a Windows-style path still meets a pattern
	// written the way every example writes it.
	slashed := filepath.ToSlash(path)
	for _, pattern := range h.Paths {
		if ok, err := doublestar.Match(pattern, slashed); ok && err == nil {
			return true
		}
	}
	return false
}

// toolPath extracts the file a tool call is about, or "" when it is not about
// one. Only the "path" argument counts: it is the name every file tool uses,
// and guessing at other arguments would make a hook fire on calls the person
// did not configure it for.
func toolPath(args map[string]any) string {
	p, _ := args["path"].(string)
	return p
}

// config is the on-disk shape: {"hooks": [...]}.
type config struct {
	Hooks []Hook `json:"hooks"`
}

var registry = struct {
	mu    sync.RWMutex
	hooks []Hook
}{}

// Load reads hook definitions from the given files, in order, and replaces
// whatever was loaded before. Missing files are not an error — hooks are
// opt-in. A malformed file IS reported: silently ignoring it would leave the
// user believing a protection is active when it is not.
func Load(paths ...string) []error {
	var all []Hook
	var errs []error

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("hooks: cannot read %s: %w", path, err))
			}
			continue
		}
		var cfg config
		if err := json.Unmarshal(data, &cfg); err != nil {
			errs = append(errs, fmt.Errorf("hooks: %s is not valid JSON: %w", path, err))
			continue
		}
		for i, h := range cfg.Hooks {
			if h.Command == "" {
				errs = append(errs, fmt.Errorf("hooks: %s entry %d has no command", path, i))
				continue
			}
			if h.Event != EventPreTool && h.Event != EventPostTool {
				errs = append(errs, fmt.Errorf("hooks: %s entry %d has unknown event %q (want %q or %q)", path, i, h.Event, EventPreTool, EventPostTool))
				continue
			}
			// A malformed glob is reported rather than left to match
			// nothing: an inactive hook the user believes is active is the
			// exact failure this package is meant to avoid.
			badGlob := false
			for _, pattern := range h.Paths {
				if !doublestar.ValidatePattern(pattern) {
					errs = append(errs, fmt.Errorf("hooks: %s entry %d has invalid path pattern %q", path, i, pattern))
					badGlob = true
				}
			}
			if badGlob {
				continue
			}
			all = append(all, h)
		}
	}

	registry.mu.Lock()
	registry.hooks = all
	registry.mu.Unlock()
	return errs
}

// DefaultPaths returns the config files Load reads, global first so that a
// project's own hooks are appended after (and therefore run after) the
// user's. Both run: hooks are additive, and a project cannot switch off a
// protection the user configured for themselves.
func DefaultPaths(projectDir string) []string {
	paths := []string{filepath.Join(os.Getenv("HOME"), ".tyci", "hooks.json")}
	if projectDir == "" {
		projectDir = "."
	}
	return append(paths, filepath.Join(projectDir, ".tyci", "hooks.json"))
}

// SetForTesting installs hooks directly and returns a function restoring the
// previous set.
func SetForTesting(hooks []Hook) func() {
	registry.mu.Lock()
	prev := registry.hooks
	registry.hooks = hooks
	registry.mu.Unlock()
	return func() {
		registry.mu.Lock()
		registry.hooks = prev
		registry.mu.Unlock()
	}
}

// Any reports whether any hook is configured for the event. Callers use it to
// skip building the JSON payload on the common path where nothing is hooked.
func Any(event string) bool {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, h := range registry.hooks {
		if h.Event == event {
			return true
		}
	}
	return false
}

func matching(event, tool, path string) []Hook {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	var out []Hook
	for _, h := range registry.hooks {
		if h.Event == event && h.matches(tool, path) {
			out = append(out, h)
		}
	}
	return out
}

// payload is what a hook reads on stdin.
type payload struct {
	Event   string         `json:"event"`
	Tool    string         `json:"tool"`
	Args    map[string]any `json:"args"`
	Success *bool          `json:"success,omitempty"`
	Content string         `json:"content,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// RunPre runs the pre_tool hooks for a tool. It returns blocked=true as soon
// as one exits non-zero, along with the message to hand the model in place of
// the tool result. Hooks run in configuration order and stop at the first
// veto: once the call is blocked, running the rest would only produce advice
// about something that is not going to happen.
func RunPre(ctx context.Context, tool string, args map[string]any) (blocked bool, message string) {
	hooks := matching(EventPreTool, tool, toolPath(args))
	if len(hooks) == 0 {
		return false, ""
	}
	p := payload{Event: EventPreTool, Tool: tool, Args: args}

	for _, h := range hooks {
		out, err := run(ctx, h, p)
		if err == nil {
			continue
		}
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return true, fmt.Sprintf("blocked by the %q hook: %s", h.label(), msg)
	}
	return false, ""
}

// RunPost runs the post_tool hooks for a tool that has already executed.
//
// It returns a note to append to the tool result and, when a blocking hook
// exited non-zero, fail=true. Note that the tool's own effects have already
// happened by this point: a blocking post hook does not undo the write, it
// only makes sure the model treats the step as unfinished. The note says so,
// because a model told "write failed" would otherwise redo the write.
//
// Unlike RunPre, every hook runs: they are reporting on the same completed
// action, and a formatter's complaint should not hide a linter's.
func RunPost(ctx context.Context, tool string, args map[string]any, success bool, content, errText string) (note string, fail bool) {
	hooks := matching(EventPostTool, tool, toolPath(args))
	if len(hooks) == 0 {
		return "", false
	}
	p := payload{Event: EventPostTool, Tool: tool, Args: args, Success: &success, Content: content, Error: errText}

	var b strings.Builder
	for _, h := range hooks {
		out, err := run(ctx, h, p)
		out = strings.TrimSpace(out)
		if err == nil && out == "" {
			continue // the common case: the check passed and had nothing to say
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		switch {
		case err != nil && h.Blocking:
			fail = true
			fmt.Fprintf(&b, "[hook %s reported a problem — the %s call itself completed, so fix what is reported instead of repeating it]", h.label(), tool)
		case err != nil:
			fmt.Fprintf(&b, "[hook %s reported a problem]", h.label())
		default:
			fmt.Fprintf(&b, "[hook %s]", h.label())
		}
		if out != "" {
			b.WriteString("\n")
			b.WriteString(out)
		} else if err != nil {
			b.WriteString("\n")
			b.WriteString(err.Error())
		}
	}
	return b.String(), fail
}

// run executes one hook and returns its combined output. The error is
// non-nil when the hook exited non-zero, timed out, or could not start —
// callers treat all three the same way, because from their point of view the
// hook did not pass.
func run(ctx context.Context, h Hook, p payload) (string, error) {
	timeout := time.Duration(h.Timeout) * time.Second
	if h.Timeout <= 0 {
		timeout = defaultTimeoutSec * time.Second
	}

	// Detached from the caller's deadline on purpose for the post event: a
	// tool call that is being cancelled still wants its "record what
	// happened" hooks to run. Cancellation of the whole session still
	// applies, since ctx's parent chain is preserved.
	hookCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("cannot encode hook payload: %w", err)
	}

	cmd := exec.CommandContext(hookCtx, "sh", "-c", h.Command)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = append(os.Environ(), hookEnv(p)...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()

	out := buf.String()
	if len(out) > maxHookOutput {
		out = out[:maxHookOutput] + fmt.Sprintf("\n[hook output truncated at %d bytes]", maxHookOutput)
	}

	if hookCtx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("hook timed out after %s", timeout)
	}
	return out, runErr
}

// hookEnv provides the handful of values a one-line hook actually needs, so
// that "gofmt -w $TYCI_TOOL_PATH" works without parsing the JSON on stdin.
// Anything more structured is available there.
func hookEnv(p payload) []string {
	env := []string{
		"TYCI_HOOK_EVENT=" + p.Event,
		"TYCI_TOOL=" + p.Tool,
	}
	if path, ok := p.Args["path"].(string); ok {
		env = append(env, "TYCI_TOOL_PATH="+path)
	}
	if cmdStr, ok := p.Args["command"].(string); ok {
		env = append(env, "TYCI_TOOL_COMMAND="+cmdStr)
	}
	if p.Success != nil {
		env = append(env, fmt.Sprintf("TYCI_TOOL_SUCCESS=%t", *p.Success))
	}
	return env
}

func firstWords(s string, max int) string {
	s = strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
