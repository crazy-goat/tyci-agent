package main

import (
	"os"
	"path/filepath"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
)

// openChildSession opens the JSONL file of one subagent child and writes msgs
// to it. Child files live in the "agents" subdirectory of the project's session
// directory, so "tyci session list" (which reads the directory flat) does not
// show them. It returns nil when the file cannot be opened: a child must never
// fail because its transcript could not be written.
func openChildSession(msgs []connector.Message, model, provider string) *session.Session {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	p, err := session.DefaultPath(cwd)
	if err != nil {
		return nil
	}
	p = filepath.Join(filepath.Dir(p), "agents", filepath.Base(p))
	s, err := session.Open(p, cwd, model, provider)
	if err != nil {
		return nil
	}
	writeChildMessages(s, msgs)
	return s
}

// reopenChildSession opens the file of an earlier child again so a resumed
// conversation appends to it. The child closes its file when a run ends.
func reopenChildSession(old *session.Session, model, provider string) *session.Session {
	if old == nil {
		return nil
	}
	cwd, _ := os.Getwd()
	s, err := session.Open(old.Path(), cwd, model, provider)
	if err != nil {
		return nil
	}
	return s
}

// writeChildMessages appends msgs to the child's session file.
func writeChildMessages(s *session.Session, msgs []connector.Message) {
	for _, m := range msgs {
		_ = s.WriteMessage(m.Role, session.ContentBlocksFromConnector(m.Content), nil)
	}
}
