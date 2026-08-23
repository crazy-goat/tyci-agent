// Package trust answers one question: is this project allowed to run its
// own code?
//
// A project-local .tyci directory can carry code tyci will execute without
// the user ever typing a command for it: hooks.json runs commands through
// `sh -c` around every tool call, and Lua tools (.tyci/tools/*.lua) can shell
// out via ctx.run. That is exactly the shape of trust an editor's "do you
// trust the files in this folder" prompt exists for, and it gets the same
// answer here: one question per project, asked once, not a permission
// prompt per action (that shape is deliberately rejected for this project —
// see TODO.md item 23).
//
// The trust list lives in ~/.tyci/trust.json — global by construction, so a
// project can never declare itself trusted from inside itself — and is
// keyed by the git toplevel (session.ProjectKey / gitinfo.ProjectRoot), so a
// subdirectory or a linked worktree of an already-decided project inherits
// the decision instead of asking again.
//
// Re-ask policy: per-project, not per-content. Once a project is trusted,
// trust.json is not invalidated by a later change to hooks.json or
// .tyci/tools/ — trusting a project is trusting whoever controls that
// repository, not auditing one snapshot of it. Re-asking on every content
// change would (a) add real machinery — hashing hooks.json, every .lua
// script under .tyci/tools/, and any future local mcp.json, plus tracking
// what "changed" means across renames — for a guarantee it cannot actually
// deliver: an attacker who can add a malicious hook after trust was granted
// can just as easily add it, wait for the re-trust prompt to be approved
// out of habit, and be in exactly the position content-hashing was meant to
// prevent. (b) it directly fights the fatigue problem this feature exists
// to avoid — see the per-tool-permission-prompt rejection this design is
// explicitly not repeating. A person who already decided to trust a
// coworker's repository should not be re-litigating that decision every
// time the coworker edits a hook.
package trust

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record is one project's trust decision.
type Record struct {
	Trusted   bool      `json:"trusted"`
	DecidedAt time.Time `json:"decided_at"`
}

// file is trust.json's on-disk shape, keyed by project root (as returned by
// session.ProjectKey / gitinfo.ProjectRoot).
type file struct {
	Projects map[string]Record `json:"projects"`
}

// mu serializes this process's reads and writes of trust.json. It does not
// protect against a second tyci process racing the same file — no other
// ~/.tyci config file in this codebase does either, and the failure mode
// (last writer wins on one project's boolean) is the same low-stakes shape
// as config.json or mcp.json being edited by two processes at once.
var mu sync.Mutex

// Path returns the path to trust.json.
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("trust: cannot determine home directory: %w", err)
		}
	}
	return filepath.Join(home, ".tyci", "trust.json"), nil
}

func load() (file, error) {
	empty := file{Projects: map[string]Record{}}
	path, err := Path()
	if err != nil {
		return empty, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, err
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return empty, fmt.Errorf("trust: %s is not valid JSON: %w", path, err)
	}
	if f.Projects == nil {
		f.Projects = map[string]Record{}
	}
	return f, nil
}

func save(f file) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Lookup returns the recorded trust decision for root, and whether a
// decision has been recorded at all. root should already be resolved to a
// project key (session.ProjectKey), not a raw cwd.
func Lookup(root string) (trusted bool, known bool, err error) {
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		return false, false, err
	}
	rec, ok := f.Projects[root]
	return rec.Trusted, ok, nil
}

// SetTrusted records a trust decision for root, overwriting any previous
// one.
func SetTrusted(root string, trusted bool) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := load()
	if err != nil {
		// A corrupt trust.json must not block recording a fresh decision —
		// start clean rather than refusing forever because of one bad file.
		f = file{Projects: map[string]Record{}}
	}
	f.Projects[root] = Record{Trusted: trusted, DecidedAt: time.Now()}
	return save(f)
}

// Prompter asks whether root should be trusted and returns the answer.
// Swappable so tests can drive Decide without a live terminal.
type Prompter func(root string) (bool, error)

// StdioPrompt is the production Prompter: a single blocking yes/no question
// on the controlling terminal. Callers must invoke it before anything else
// takes over stdin/stdout (the TUI's alternate screen, a readline prompt).
func StdioPrompt(root string) (bool, error) {
	fmt.Fprintf(os.Stderr,
		"tyci: is this project trusted?\n"+
			"  %s\n"+
			"  Project-local hooks (.tyci/hooks.json) and Lua tools (.tyci/tools/) can run\n"+
			"  arbitrary shell commands. Say yes only if you trust this repository's\n"+
			"  contents. This is asked once and remembered in ~/.tyci/trust.json.\n"+
			"Trust this project? [y/N] ", root)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// Decide resolves whether root is a trusted project, asking at most once per
// project ever (see the re-ask policy in the package doc comment).
//
// interactive distinguishes the two modes this must support:
//   - interactive (console, tui): an unrecorded project blocks once on
//     prompt (StdioPrompt unless prompt overrides it), and the answer is
//     persisted so this project is never asked again.
//   - non-interactive (`tyci run`, cron): must never block. An unrecorded
//     project is treated as untrusted for this call only — nothing is
//     written, so the first interactive run still asks for real.
//
// asked reports whether prompt was actually invoked, true only when
// interactive and the project had no recorded decision yet.
func Decide(root string, interactive bool, prompt Prompter) (trusted bool, asked bool, err error) {
	if root == "" {
		return false, false, nil
	}
	recorded, known, err := Lookup(root)
	if err != nil {
		return false, false, err
	}
	if known {
		return recorded, false, nil
	}
	if !interactive {
		return false, false, nil
	}
	if prompt == nil {
		prompt = StdioPrompt
	}
	answer, err := prompt(root)
	if err != nil {
		return false, true, err
	}
	if err := SetTrusted(root, answer); err != nil {
		return answer, true, err
	}
	return answer, true, nil
}
