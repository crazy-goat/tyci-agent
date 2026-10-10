package main

import (
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/tools"
)

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
	out := make([]tools.SessionInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, tools.SessionInfo{
			Path:        e.Path,
			Title:       e.Title,
			FirstPrompt: e.FirstPrompt,
			Modified:    e.ModTime.Time(),
		})
	}
	return out, nil
}
