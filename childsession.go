package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/internal/runlog"
	"github.com/crazy-goat/tyci-agent/session"
)

// openChildSession opens the JSONL file of one subagent child and writes msgs
// to it. Child files live in the "agents" subdirectory of the project's session
// directory, so "tyci session list" (which reads the directory flat) does not
// show them. It returns nil when the file cannot be opened: a child must never
// fail because its transcript could not be written.
func openChildSession(msgs []connector.Message, model, provider, jobID string) *session.Session {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	p, err := session.DefaultPath(cwd)
	if err != nil {
		return nil
	}
	p = childPath(p, jobID)
	s, err := session.Open(p, cwd, model, provider)
	if err != nil {
		return nil
	}
	writeChildMessages(s, msgs)
	return s
}

// childPath puts the file of job jobID in the "agents" subdirectory and adds
// the job id to the file name, so a user can map a job to its file.
func childPath(p, jobID string) string {
	id := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == ' ' {
			return '_'
		}
		return r
	}, jobID)
	base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
	return filepath.Join(filepath.Dir(p), "agents", base+"_"+id+".jsonl")
}

// forkChildSession starts the file of a resumed job. It copies the file of the
// earlier child to a new path and opens the copy, so two resumes of one job
// never share a file. The earlier file stays unchanged.
func forkChildSession(old *session.Session, model, provider, jobID string) *session.Session {
	if old == nil {
		return nil
	}
	data, err := os.ReadFile(old.Path())
	if err != nil {
		return nil
	}
	cwd, _ := os.Getwd()
	dst, err := session.DefaultPath(cwd)
	if err != nil {
		return nil
	}
	dst = childPath(dst, jobID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return nil
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return nil
	}
	s, err := session.Open(dst, cwd, model, provider)
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

// openRunTranscript opens the redacted transcript at path (runlog.Path) and
// writes msgs to it. It returns nil on error: a child must never fail because
// its transcript could not be written.
func openRunTranscript(path string, msgs []connector.Message, model, provider string) *runlog.Writer {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	w, err := runlog.OpenPath(path, cwd, model, provider)
	if err != nil {
		return nil
	}
	for _, m := range msgs {
		_ = w.Write(m.Role, session.ContentBlocksFromConnector(m.Content))
	}
	return w
}
