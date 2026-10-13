package bus

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/redact"
)

// journalPath returns a journal file path in a new temporary directory.
func journalPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "bus.jsonl")
}

// readJournal returns the messages of the journal file at path. It fails
// when a line does not decode.
func readJournal(t *testing.T, path string) []Message {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var out []Message
	for line := range bytes.Lines(data) {
		var m Message
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("journal line %q does not decode: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// failWriter fails every write.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestJournal_WritesOnlyDurable(t *testing.T) {
	path := journalPath(t)
	b := New(WithJournal(path))
	defer b.Close()
	mustPublish(t, b, kindDurable, item{ID: "a", N: 1})
	mustPublish(t, b, kindLatest, item{ID: "k", N: 2})
	mustPublish(t, b, kindOther, item{ID: "b", N: 3})

	msgs := readJournal(t, path)
	for _, m := range msgs {
		if m.Kind == kindLatest {
			t.Fatalf("Latest message in journal: %+v", m)
		}
	}
	got := decodeItems(t, msgs)
	if want := []item{{ID: "a", N: 1}, {ID: "b", N: 3}}; !slices.Equal(got, want) {
		t.Fatalf("journal payloads = %+v, want %+v", got, want)
	}
}

func TestJournal_RedactsSecrets(t *testing.T) {
	path := journalPath(t)
	token := "ghp_" + strings.Repeat("a1", 18)
	redactLine := func(line []byte) []byte { return []byte(redact.Redact(string(line))) }
	b := New(WithJournal(path), WithRedactor(redactLine))
	defer b.Close()
	mustPublish(t, b, kindDurable, item{ID: "token " + token + " OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz"})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, secret := range []string{token, "sk-abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("journal contains secret %q: %s", secret, data)
		}
	}
	if !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("journal has no mask: %s", data)
	}
	// The redacted line must still decode, or the next Seq is not recovered.
	if msgs := readJournal(t, path); len(msgs) != 1 {
		t.Fatalf("journal has %d lines, want 1", len(msgs))
	}
}

func TestJournal_FileMode0600(t *testing.T) {
	path := journalPath(t)
	b := New(WithJournal(path))
	defer b.Close()
	mustPublish(t, b, kindDurable, item{ID: "a"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("journal mode = %o, want 600", perm)
	}
}

func TestJournal_ExistingFileGetsMode0600(t *testing.T) {
	path := journalPath(t)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	b := New(WithJournal(path))
	defer b.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("journal mode = %o, want 600", perm)
	}
}

func TestJournal_ReplaysSameMessages(t *testing.T) {
	path := journalPath(t)
	b := New(WithJournal(path))
	defer b.Close()
	s := b.Subscribe("x", Filter{To: agent("x")})
	if _, err := Publish(b, kindDurable, agent("y"), agent("x"), OriginAgent, item{ID: "a", N: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	mustPublish(t, b, kindDurable, item{ID: "b", N: 2})
	drained := s.Drain()

	lines := readJournal(t, path)
	if len(lines) != len(drained) {
		t.Fatalf("journal has %d lines, drained %d messages", len(lines), len(drained))
	}
	for i, got := range lines {
		want := drained[i]
		if got.Seq != want.Seq || got.Kind != want.Kind || got.From != want.From ||
			got.To != want.To || got.Origin != want.Origin || !bytes.Equal(got.Payload, want.Payload) ||
			!got.At.Equal(want.At) {
			t.Fatalf("line %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestJournal_WriteFailure_DoesNotBlockOrDropInMemory(t *testing.T) {
	b := New(WithJournal(journalPath(t)))
	defer b.Close()
	var logs bytes.Buffer
	b.journal.w = failWriter{}
	b.journal.logw = &logs
	s := b.Subscribe("x", Filter{To: agent("x")})

	for i := range 3 {
		mustPublish(t, b, kindDurable, item{ID: "a", N: i})
	}
	if got := s.Drain(); len(got) != 3 {
		t.Fatalf("Drain after write failure has %d messages, want 3", len(got))
	}
	if n := strings.Count(logs.String(), "journal write failed"); n != 1 {
		t.Fatalf("write failure logged %d times, want once: %q", n, logs.String())
	}
}

func TestJournal_OpenFailure_RunsInMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "bus.jsonl")
	b := New(WithJournal(path))
	defer b.Close()

	if seq := mustPublish(t, b, kindDurable, item{ID: "a"}); seq != 1 {
		t.Fatalf("Seq = %d, want 1", seq)
	}
}

// TestJournal_OpenDoesNotReadOrTruncate checks that opening a journal leaves
// the existing bytes alone, including a partial last line, and that Seq starts
// at 1 for each journal file. Each process has its own file.
func TestJournal_OpenDoesNotReadOrTruncate(t *testing.T) {
	path := journalPath(t)
	old := []byte(`{"seq":9}` + "\n" + `{"seq":1`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(WithJournal(path))
	defer b.Close()
	if seq := mustPublish(t, b, kindDurable, item{ID: "d"}); seq != 1 {
		t.Fatalf("first Seq = %d, want 1", seq)
	}
	b.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, old) {
		t.Fatalf("open changed the existing bytes: %q", data)
	}
}
