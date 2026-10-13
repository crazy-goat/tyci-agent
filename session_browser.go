package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

// sessionReadTextLimit is the most runes of one message that session_read returns.
const sessionReadTextLimit = 2000

// sessionBrowserAdapter satisfies tools.SessionBrowser with the session
// package, so the tools package does not list sessions itself.
type sessionBrowserAdapter struct{}

func (sessionBrowserAdapter) List(cwd string, all bool) ([]tools.SessionInfo, error) {
	var entries []session.ResumeEntry
	var err error
	if all {
		entries, err = session.ResumeEntriesAll()
	} else {
		entries, err = session.ResumeEntries(cwd)
	}
	if err != nil {
		return nil, err
	}
	// With all, a session of another project is listed but Read refuses it,
	// so it is marked. The current project is the directory of cwd.
	var currentDir string
	if all {
		currentDir, err = session.SessionDir(cwd)
		if err != nil {
			return nil, err
		}
	}
	out := make([]tools.SessionInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, tools.SessionInfo{
			Path:         e.Path,
			Title:        e.Title,
			FirstPrompt:  e.FirstPrompt,
			Modified:     e.ModTime.Time(),
			OtherProject: all && filepath.Dir(e.Path) != currentDir,
		})
	}
	return out, nil
}

// Read refuses a path that session_list does not return for the current
// project. It keeps the last maxMessages user and assistant messages that
// have text. Tool calls, tool results and thinking blocks are skipped.
func (sessionBrowserAdapter) Read(path string, maxMessages int) (string, []tools.SessionMessage, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, false, err
	}
	entries, err := session.ResumeEntries(cwd)
	if err != nil {
		return "", nil, false, err
	}
	var title string
	listed := false
	for _, e := range entries {
		if e.Path == path {
			title = e.Title
			listed = true
			break
		}
	}
	if !listed {
		return "", nil, false, fmt.Errorf("session %q is not in the current project; use session_list first", path)
	}

	_, msgs, _, _, err := session.LoadForReplay(path)
	if err != nil {
		return "", nil, false, err
	}
	var texts []tools.SessionMessage
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := messageText(m)
		if text == "" {
			continue
		}
		texts = append(texts, tools.SessionMessage{Role: m.Role, Text: cutRunes(text, sessionReadTextLimit)})
	}
	truncated := len(texts) > maxMessages
	if truncated {
		texts = texts[len(texts)-maxMessages:]
	}
	return title, texts, truncated, nil
}

// messageText joins the text blocks of a message with a blank line.
func messageText(m connector.Message) string {
	var parts []string
	for _, b := range m.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// cutRunes returns the first limit runes of s and "..." when it cuts.
func cutRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "..."
}
