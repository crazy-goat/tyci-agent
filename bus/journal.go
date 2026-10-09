package bus

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
)

// journal appends Durable messages to a file, one JSON object per line.
type journal struct {
	// redact runs on each line before it is written. nil means no redaction.
	redact func([]byte) []byte
	// logw receives the one-time report of a write failure.
	logw io.Writer

	mu     sync.Mutex
	f      *os.File
	w      io.Writer
	failed bool
	closed bool
}

// openJournal opens the journal file at path for append. It removes an
// incomplete last line, so that the next line starts on a new line. It
// returns the journal and the highest Seq that the file holds.
func openJournal(path string, redact func([]byte) []byte) (*journal, uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, 0, err
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	if end < len(data) {
		if err := os.Truncate(path, int64(end)); err != nil {
			return nil, 0, err
		}
	}

	// Take the highest Seq, not the last line. Writes happen after bus.mu is
	// released, so two publishers can write their lines out of Seq order.
	var last uint64
	for line := range bytes.Lines(data[:end]) {
		var m Message
		if json.Unmarshal(line, &m) == nil && m.Seq > last {
			last = m.Seq
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, 0, err
	}
	// The mode of OpenFile applies only to a new file. Set it on an old one too.
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return &journal{redact: redact, logw: os.Stderr, f: f, w: f}, last, nil
}

// write appends m as one redacted line. A write failure is logged once.
// The message stays in memory, and write never blocks the caller for long.
func (j *journal) write(m Message) {
	line, err := json.Marshal(m)
	if err == nil {
		if j.redact != nil {
			line = j.redact(line)
		}
		line = append(line, '\n')
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	if err == nil {
		_, err = j.w.Write(line)
	}
	if err != nil {
		j.report(err)
	}
}

// report logs err once for the whole journal. The caller holds j.mu.
func (j *journal) report(err error) {
	if j.failed {
		return
	}
	j.failed = true
	fmt.Fprintf(j.logw, "bus: journal write failed, messages stay in memory only: %v\n", err)
}

// close closes the file. Later writes do nothing. close can be called more
// than once.
func (j *journal) close() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	j.closed = true
	_ = j.f.Close()
}
