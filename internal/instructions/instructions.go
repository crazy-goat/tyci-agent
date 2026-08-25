// Package instructions loads the standing, project-specific context an agent
// should start every session with, and manages the notes it writes for its
// own future sessions.
//
// Two sources, because they answer two different questions:
//
//   - AGENTS.md — what a person wants every agent working here to know: how
//     to build, how to run the tests, which layering rules exist, what not to
//     touch. Hand-written and reviewed, so it is authoritative.
//   - .tyci/memory/*.md — what the agent worked out for itself and wants to
//     remember. Written by the "memory" tool during a session and read back at
//     the start of the next one.
//
// Both end up in the system prompt. Without them a session starts knowing
// nothing but the working directory, which means the same explanation gets
// typed again every time.
//
// The package deliberately depends on nothing but the standard library: both
// the prompt builder (providers) and the tool that writes notes (tools) import
// it, and neither may import the other.
package instructions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrValidation = errors.New("invalid memory note input")

const (
	// FileName is the project instructions file. AGENTS.md rather than a
	// tyci-specific name: it is a convention several agents now read, so one
	// file serves them all.
	FileName = "AGENTS.md"

	// MemoryDirName is where notes live, relative to the project root.
	MemoryDirName = ".tyci/memory"

	// maxTotalBytes caps everything this package contributes to the system
	// prompt. The prompt is re-sent on every single request, so a runaway
	// AGENTS.md would be paid for over and over — and would crowd out the
	// conversation it is supposed to support.
	maxTotalBytes = 32 * 1024

	// maxMemoryFileBytes caps one note. A note is a conclusion, not a
	// transcript; anything longer belongs in a file in the repository.
	maxMemoryFileBytes = 8 * 1024

	// maxMemoryFiles bounds how many notes are kept. Past this the model is
	// accumulating rather than curating, and every one of them is paid for on
	// every request.
	maxMemoryFiles = 50
)

// Sources returns the AGENTS.md files to load, in the order they should
// appear: the user's global one first, then the project's, so a project can
// add to (and, by being read later, effectively override) the global one.
//
// The project file is the nearest AGENTS.md at or above the working
// directory, searching no further up than the repository root — that is the
// file a person would point at if asked "which one applies here?". The search
// stops at the directory holding .git so that running tyci in a subdirectory
// still finds the repository's file, while a stray AGENTS.md in a parent
// directory outside the repository is never silently picked up.
func Sources(home, cwd string) []string {
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, ".tyci", FileName))
	}
	if project := findProjectFile(cwd); project != "" {
		out = append(out, project)
	}
	return out
}

func findProjectFile(cwd string) string {
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, FileName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
		// The repository root is the last place worth looking: above it we are
		// in someone else's project, or in a home directory whose AGENTS.md is
		// already covered by the global source.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Load builds the block to append to the system prompt, or "" when there is
// nothing to say. cwd is the project root; home may be empty.
func Load(home, cwd string) string {
	var b strings.Builder

	for _, path := range Sources(home, cwd) {
		data, err := os.ReadFile(path)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s ---\n%s\n", displayPath(cwd, path), strings.TrimSpace(string(data)))
	}

	notes := loadMemories(cwd)
	if notes != "" {
		b.WriteString(notes)
	}

	if b.Len() == 0 {
		return ""
	}

	body := b.String()
	if len(body) > maxTotalBytes {
		body = body[:maxTotalBytes] + fmt.Sprintf("\n[project instructions truncated at %d bytes]\n", maxTotalBytes)
	}

	// The framing matters as much as the content. Without it the model treats
	// this as background prose; with it, it knows these lines outrank its own
	// habits and that the notes are its own from a previous session.
	return "\n\nProject instructions. AGENTS.md is written by the person you work for: where it conflicts with your defaults, it wins, and you do not need to re-derive what it already tells you. Any \"note\" blocks are your own, written in an earlier session with the \"memory\" tool — treat them as things you already worked out, and correct one with memory(action=\"write\") if you find it is now wrong.\n" + body
}

func loadMemories(cwd string) string {
	dir := MemoryDir(cwd)
	names, err := List(cwd)
	if err != nil || len(names) == 0 {
		return ""
	}

	var b strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name+".md"))
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n--- note: %s ---\n%s\n", name, strings.TrimSpace(string(data)))
	}
	return b.String()
}

// displayPath shows a project file relative to the project and leaves
// anything outside it absolute, so the model can tell the two sources apart.
func displayPath(cwd, path string) string {
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}

// ---------------------------------------------------------------------------
// Notes
// ---------------------------------------------------------------------------

// MemoryDir returns the notes directory for a project root.
func MemoryDir(cwd string) string {
	return filepath.Join(cwd, MemoryDirName)
}

// SanitizeName turns a requested note name into a safe file stem, or returns
// an error when nothing usable is left.
//
// Restrictive on purpose: the name becomes a filename, and the model chooses
// it. Allowing separators or dots would make "../../etc/x" and "..md" the
// tool's problem instead of the caller's.
func SanitizeName(name string) (string, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		// Underscores and spaces collapse to "-" so that "test_command",
		// "test command" and "test-command" cannot become three notes about
		// the same thing.
		case r == '-', r == '_', r == ' ':
			b.WriteRune('-')
		}
	}
	clean := strings.Trim(b.String(), "-")
	for strings.Contains(clean, "--") {
		clean = strings.ReplaceAll(clean, "--", "-")
	}
	if clean == "" {
		return "", fmt.Errorf("%w: name must contain letters or digits", ErrValidation)
	}
	if len(clean) > 60 {
		clean = strings.Trim(clean[:60], "-")
	}
	return clean, nil
}

// List returns the note names in a project, sorted, without the .md suffix.
func List(cwd string) ([]string, error) {
	entries, err := os.ReadDir(MemoryDir(cwd))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}

// Read returns one note's content.
func Read(cwd, name string) (string, error) {
	clean, err := SanitizeName(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(MemoryDir(cwd), clean+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no note called %q", clean)
		}
		return "", err
	}
	return string(data), nil
}

// Write creates or replaces a note and returns the name it was stored under.
func Write(cwd, name, content string) (string, error) {
	clean, err := SanitizeName(name)
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", fmt.Errorf("%w: content is empty; use action=\"delete\" to remove a note", ErrValidation)
	}
	if len(content) > maxMemoryFileBytes {
		return "", fmt.Errorf("%w: note is %d bytes, the limit is %d — a note is a conclusion, not a transcript", ErrValidation, len(content), maxMemoryFileBytes)
	}

	dir := MemoryDir(cwd)
	existing, err := List(cwd)
	if err != nil {
		return "", err
	}
	// Replacing an existing note is always allowed; the cap only stops the
	// collection from growing without bound.
	if len(existing) >= maxMemoryFiles && !contains(existing, clean) {
		return "", fmt.Errorf("there are already %d notes, the limit is %d — delete one that is no longer true instead of adding another", len(existing), maxMemoryFiles)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, clean+".md"), []byte(content+"\n"), 0o644); err != nil {
		return "", err
	}
	return clean, nil
}

// Delete removes a note. A missing note is an error: silently succeeding
// would let the model believe it had corrected something it had not.
func Delete(cwd, name string) (string, error) {
	clean, err := SanitizeName(name)
	if err != nil {
		return "", err
	}
	path := filepath.Join(MemoryDir(cwd), clean+".md")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no note called %q", clean)
		}
		return "", err
	}
	return clean, nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
