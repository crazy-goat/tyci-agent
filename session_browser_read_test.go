package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/session"
)

// isolatedSessionHome points HOME at a temp dir, makes a temp cwd the working
// directory and writes one session file into the session directory of that
// cwd. It returns the file path.
func isolatedSessionHome(t *testing.T, lines []string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return writeSessionFile(t, cwd, lines)
}

// writeSessionFile writes one session file into the session directory of cwd
// and returns its path.
func writeSessionFile(t *testing.T, cwd string, lines []string) string {
	t.Helper()
	dir, err := session.SessionDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.jsonl")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSessionBrowserRead_RefusesSessionOfOtherProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	otherCwd := t.TempDir()
	otherPath := writeSessionFile(t, otherCwd, []string{
		`{"type":"session","id":"o","projectRoot":"` + otherCwd + `"}`,
		`{"type":"message","id":"o1","message":{"role":"user","content":[{"type":"text","text":"secret of the other project"}]}}`,
	})
	// The current project is a different directory with no sessions.
	t.Chdir(t.TempDir())

	title, msgs, _, err := sessionBrowserAdapter{}.Read(otherPath, 20)
	if err == nil || !strings.Contains(err.Error(), "current project") {
		t.Fatalf("want refusal naming the current project, got err=%v", err)
	}
	if title != "" || len(msgs) != 0 {
		t.Errorf("refused read returned content: title=%q msgs=%+v", title, msgs)
	}
}

func TestSessionBrowserRead_ReadsSessionOfCurrentProject(t *testing.T) {
	path := isolatedSessionHome(t, []string{
		`{"type":"session","id":"a","projectRoot":"/x"}`,
		`{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"current project question"}]}}`,
	})

	_, msgs, _, err := sessionBrowserAdapter{}.Read(path, 20)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Text != "current project question" {
		t.Errorf("messages = %+v, want the current project question", msgs)
	}
}

func TestSessionBrowserRead_RefusesUnlistedPath(t *testing.T) {
	isolatedSessionHome(t, []string{
		`{"type":"session","id":"a","projectRoot":"/x"}`,
	})
	_, _, _, err := sessionBrowserAdapter{}.Read("/tmp/nope.jsonl", 5)
	if err == nil || !strings.Contains(err.Error(), "session_list") {
		t.Errorf("want refusal naming session_list, got %v", err)
	}
}

func TestSessionBrowserRead_ReturnsLastMessagesOnly(t *testing.T) {
	lines := []string{
		`{"type":"session","id":"a","projectRoot":"/x"}`,
	}
	for i := 1; i <= 5; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"type":"message","id":"m%d","message":{"role":"user","content":[{"type":"text","text":"msg %d"}]}}`, i, i))
	}
	lines = append(lines, `{"type":"title","id":"a","timestamp":"2026-10-10T08:00:00Z","title":"my title"}`)
	path := isolatedSessionHome(t, lines)

	title, msgs, truncated, err := sessionBrowserAdapter{}.Read(path, 2)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if title != "my title" {
		t.Errorf("title = %q, want %q", title, "my title")
	}
	if !truncated {
		t.Error("truncated = false, want true")
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Text != "msg 4" || msgs[1].Text != "msg 5" {
		t.Errorf("messages = %+v, want msg 4 and msg 5", msgs)
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want user", msgs[0].Role)
	}
}

func TestSessionBrowserRead_SkipsToolBlocks(t *testing.T) {
	path := isolatedSessionHome(t, []string{
		`{"type":"session","id":"a","projectRoot":"/x"}`,
		`{"type":"message","id":"m1","message":{"role":"user","content":[{"type":"text","text":"question"}]}}`,
		`{"type":"message","id":"m2","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret reasoning"},{"type":"toolCall","id":"c1","name":"bash","arguments":{"cmd":"ls"}},{"type":"text","text":"answer"}]}}`,
		`{"type":"message","id":"m3","message":{"role":"user","content":[{"type":"toolResult","toolCallId":"c1","toolName":"bash","text":"file.txt"}]}}`,
	})

	_, msgs, truncated, err := sessionBrowserAdapter{}.Read(path, 20)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if truncated {
		t.Error("truncated = true, want false")
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Text != "question" || msgs[1].Text != "answer" {
		t.Errorf("messages = %+v", msgs)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "secret") || strings.Contains(m.Text, "file.txt") || strings.Contains(m.Text, "ls") {
			t.Errorf("tool or thinking text leaked: %+v", m)
		}
	}
}
