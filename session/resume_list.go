package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ResumeEntry is one row in the /resume picker: a saved session plus the
// first user prompt shown in the picker so the user can pick by glance.
// FirstPrompt is empty if the file had no user-role message yet (e.g. empty
// or only a header line) — that still gets a row, just with a placeholder.
type ResumeEntry struct {
	Path        string
	Name        string
	Size        int64
	ModTime     UnixMillis
	FirstPrompt string
}

// ResumeEntries lists sessions in the cwd-encoded dir and reads the first
// user prompt from each (best-effort; empty string if not readable). Sorted
// newest-first, matching ListEntries ordering so the picker's top row is the
// most recent session.
func ResumeEntries(cwd string) ([]ResumeEntry, error) {
	dir, err := SessionDir(cwd)
	if err != nil {
		return nil, err
	}
	return resumeEntriesInDir(dir)
}

// ResumeEntriesAll lists sessions across every project (the "--all" escape
// hatch), not just the one containing the current cwd. Used by `tyci
// session list --all` and the TUI's `/resume --all`.
func ResumeEntriesAll() ([]ResumeEntry, error) {
	dirs, err := AllProjectDirs()
	if err != nil {
		return nil, err
	}
	var out []ResumeEntry
	for _, dir := range dirs {
		entries, err := resumeEntriesInDir(dir)
		if err != nil {
			continue
		}
		out = append(out, entries...)
	}
	sortResumeEntriesDesc(out)
	return out, nil
}

// resumeEntriesInDir lists the sessions in one project directory, newest
// first. Shared by ResumeEntries (one project) and ResumeEntriesAll (every
// project).
func resumeEntriesInDir(dir string) ([]ResumeEntry, error) {
	if dir == "" {
		return nil, nil
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ResumeEntry, 0, len(files))
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".jsonl") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, f.Name())
		out = append(out, ResumeEntry{
			Path:        path,
			Name:        f.Name(),
			Size:        info.Size(),
			ModTime:     UnixMillisFromTime(info.ModTime()),
			FirstPrompt: readFirstUserPrompt(path),
		})
	}
	// Newest first.
	sortResumeEntriesDesc(out)
	return out, nil
}

// readFirstUserPrompt opens path and reads just enough of the JSONL header
// + first user message to extract the prompt text. Tolerates partial /
// corrupt files (returns "" on any error after the open). The file is read
// line by line with a buffered scanner; we stop as soon as we find a
// user-role message so cost stays bounded even for very long sessions.
func readFirstUserPrompt(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Allow long first prompts.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			// Skip non-JSON / partial line and try the next one.
			continue
		}
		if t, _ := raw["type"].(string); t != "message" {
			continue
		}
		// Session JSONL stores messages as: {"type":"message","message":{"role":"...","content":[...]}}
		// (matching MessageEvent / MessagePayload JSON tags in session.go).
		msg, _ := raw["message"].(map[string]any)
		if msg == nil {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}
		// content is an array of {type, text, ...}; collect text blocks for the
		// preview. Other block types (toolCall, toolResult, thinking) are ignored.
		// Skip user messages with no extractable text — the *prompt* is the first
		// one the user actually said, not tool plumbing.
		blocks, _ := msg["content"].([]any)
		var b strings.Builder
		for _, blk := range blocks {
			bm, ok := blk.(map[string]any)
			if !ok {
				continue
			}
			if bm["type"] != "text" {
				continue
			}
			if s, ok := bm["text"].(string); ok {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(s)
			}
		}
		text := strings.TrimSpace(b.String())
		if text != "" {
			return text
		}
	}
	return ""
}

// sortResumeEntriesDesc sorts in place by modtime desc. Local helper to
// avoid pulling sort into callers when this is the only ordering needed.
func sortResumeEntriesDesc(s []ResumeEntry) {
	// Insertion sort is fine: sessions per dir are usually tens, not
	// thousands, and we want a stable, dependency-free sort.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].ModTime > s[j-1].ModTime; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// UnixMillis is milliseconds since the unix epoch — a stable integer
// representation of a time.Time so we can sort ResumeEntries without
// importing sort. Defined here (rather than re-exported from session.go)
// to keep this file self-contained for the picker.
type UnixMillis int64

// UnixMillisFromTime converts a time.Time to milliseconds since the
// unix epoch. t's monotonic clock reading is stripped by UnixNano.
func UnixMillisFromTime(t interface{ UnixNano() int64 }) UnixMillis {
	return UnixMillis(t.UnixNano() / int64(1000*1000))
}

// Time returns the time.Time corresponding to this UnixMillis value.
// Convenience for call sites that need a renderable time (display, log
// formatting) without re-converting through time.Unix(0, ...) boilerplate.
func (u UnixMillis) Time() time.Time {
	return time.Unix(int64(u)/1000, (int64(u)%1000)*int64(time.Millisecond))
}
