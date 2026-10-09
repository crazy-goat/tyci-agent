package main

import (
	"os"
	"path/filepath"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/session"
)

// busJournalPath returns the journal file for the working directory, or ""
// when its session directory does not exist yet.
func busJournalPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir, err := session.SessionDir(wd)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join(dir, "bus.jsonl")
}

// newAppBus returns a bus that routes to JobRegistry's agent tree. It journals
// to journalPath when that is not empty, with every journal line passed
// through redact.Redact first.
func newAppBus(journalPath string) *bus.Bus {
	return bus.New(
		bus.WithTree(JobRegistry.BusTree()),
		bus.WithJournal(journalPath),
		bus.WithRedactor(redactJournalLine),
	)
}

// redactJournalLine applies the secret redaction of internal/redact to one
// journal line.
func redactJournalLine(line []byte) []byte {
	return []byte(redact.Redact(string(line)))
}
