package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/crazy-goat/tyci-agent/session"
)

// SessionRenamer names the current session. It is nil until SetSessionRenamer
// is called, which the chat TUI does. A subagent never gets session_rename.
type SessionRenamer func(title string) (string, error)

var (
	sessionRenamerMu sync.RWMutex
	sessionRenamer   SessionRenamer
)

// SetSessionRenamer wires session_rename to the chat session.
func SetSessionRenamer(fn SessionRenamer) {
	sessionRenamerMu.Lock()
	sessionRenamer = fn
	sessionRenamerMu.Unlock()
}

// getSessionRenamer copies the current SessionRenamer out under RLock, so the
// caller never holds the lock while it calls the function.
func getSessionRenamer() SessionRenamer {
	sessionRenamerMu.RLock()
	defer sessionRenamerMu.RUnlock()
	return sessionRenamer
}

// SessionRenameTool gives the current session a title. It does the same as
// the /rename slash command.
type SessionRenameTool struct{}

func (t *SessionRenameTool) Name() string { return "session_rename" }

// Run stores the title through the wired SessionRenamer and returns it.
func (t *SessionRenameTool) Run(_ context.Context, input map[string]any) ToolResult {
	title := session.CleanTitle(stringParam(input, "title", ""))
	if title == "" {
		return ToolResult{Type: "result", Success: false, Error: "session_rename: title is required"}
	}
	rename := getSessionRenamer()
	if rename == nil {
		return ToolResult{Type: "result", Success: false, Error: "session_rename is unavailable in this mode"}
	}
	stored, err := rename(title)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("session_rename: %v", err)}
	}
	title = stored
	b, err := json.Marshal(map[string]string{"title": title})
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("session_rename: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: string(b)}
}
