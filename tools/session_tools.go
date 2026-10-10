package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

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

// SessionInfo is one saved session as the session_list tool shows it.
type SessionInfo struct {
	Path        string
	Title       string
	FirstPrompt string
	Modified    time.Time
}

// SessionBrowser lists saved sessions, newest first. main wires it with
// SetSessionBrowser. The tools package does not list sessions itself, so the
// session package stays out of the tool code path.
type SessionBrowser interface {
	List(cwd string, all bool) ([]SessionInfo, error)
}

var (
	sessionBrowserMu sync.RWMutex
	sessionBrowser   SessionBrowser
)

// SetSessionBrowser wires session_list to the session store. Safe for
// concurrent use. Pass nil to unwire it.
func SetSessionBrowser(b SessionBrowser) {
	sessionBrowserMu.Lock()
	sessionBrowser = b
	sessionBrowserMu.Unlock()
}

// getSessionBrowser copies the current SessionBrowser out under RLock, so the
// caller never holds the lock while it calls the interface.
func getSessionBrowser() SessionBrowser {
	sessionBrowserMu.RLock()
	defer sessionBrowserMu.RUnlock()
	return sessionBrowser
}

const (
	sessionListDefaultLimit = 10
	sessionListMaxLimit     = 50
)

// SessionListTool lists saved sessions. A subagent never gets session_list.
type SessionListTool struct{}

func (t *SessionListTool) Name() string { return "session_list" }

// Run returns the newest sessions as a JSON array. The limit is 1 to 50 and
// defaults to 10. The title goes through session.CleanTitle, the same cleaning
// as the /resume picker.
func (t *SessionListTool) Run(_ context.Context, input map[string]any) ToolResult {
	limit := sessionListLimit(input["limit"])
	all := boolParam(input, "all", false)

	browser := getSessionBrowser()
	if browser == nil {
		return failf("session_list is unavailable in this mode")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return failf("session_list: %v", err)
	}
	list, err := browser.List(cwd, all)
	if err != nil {
		return failf("session_list: %v", err)
	}
	if len(list) > limit {
		list = list[:limit]
	}

	rows := make([]map[string]any, 0, len(list))
	for _, s := range list {
		rows = append(rows, map[string]any{
			"path":         s.Path,
			"title":        session.CleanTitle(s.Title),
			"first_prompt": s.FirstPrompt,
			"modified":     s.Modified.Format(time.RFC3339),
		})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return failf("session_list: %v", err)
	}
	return ToolResult{Type: "result", Success: true, Content: string(b)}
}

// sessionListLimit reads the limit argument. JSON numbers decode as float64.
// A missing or out-of-range value gives the default for values below 1 and
// the maximum for values above it.
func sessionListLimit(v any) int {
	n := sessionListDefaultLimit
	switch x := v.(type) {
	case float64:
		n = int(x)
	case int:
		n = x
	}
	if n < 1 {
		return sessionListDefaultLimit
	}
	if n > sessionListMaxLimit {
		return sessionListMaxLimit
	}
	return n
}
