package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/session"
)

func intPtr(n int) *int { return &n }

// backgroundResult runs a call that returns content, backgrounds it with a
// zero wait and returns the late tool.result of the dispatcher. The call
// waits for its release, so that Await cannot see it finish first.
func backgroundResult(t *testing.T, d *Dispatcher, content string) bus.ToolResult {
	t.Helper()
	release := make(chan struct{})
	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		<-release
		return content, false
	})
	if out := d.Await(context.Background(), id, 0); !out.Background {
		t.Fatalf("outcome = %+v, want background", out)
	}
	close(release)
	return waitToolResult(t, d, id)
}

// newTailDispatcher returns a dispatcher on a bus, with result_tail_bytes set
// to limit (0 means the default) and the result directory set to dir.
func newTailDispatcher(t *testing.T, limit int, dir string) *Dispatcher {
	t.Helper()
	b := bus.New()
	sub := b.Subscribe("test", bus.Filter{To: testSelf, Kinds: []bus.Kind{bus.KindToolResult}})
	t.Cleanup(sub.Close)
	d := newDispatcher(b, testSelf, sub)
	if limit > 0 {
		d.tools = ToolsConfig{ResultTailBytes: intPtr(limit)}
	}
	d.SetResultDir(dir)
	t.Cleanup(d.Close)
	return d
}

func TestTailKeepsLastBytes(t *testing.T) {
	content := strings.Repeat("a", 20*1024) + strings.Repeat("b", 20*1024)
	tail, truncated := tailResult(content, 16384)
	if !truncated {
		t.Fatal("truncated = false, want true for 40 KB")
	}
	if len(tail) != 16384 {
		t.Fatalf("tail length = %d, want 16384", len(tail))
	}
	if tail != content[len(content)-16384:] {
		t.Error("tail is not the last 16384 bytes")
	}
}

func TestTailUTF8Boundary(t *testing.T) {
	// Each "é" is 2 bytes. A cut at an odd offset starts inside a rune.
	content := "x" + strings.Repeat("é", 100)
	tail, truncated := tailResult(content, 31)
	if !truncated {
		t.Fatal("truncated = false, want true")
	}
	if !utf8.ValidString(tail) {
		t.Fatalf("tail is not valid UTF-8: %q", tail)
	}
	if len(tail) > 31 {
		t.Errorf("tail length = %d, want at most 31", len(tail))
	}
}

func TestTailSmallUntouched(t *testing.T) {
	content := strings.Repeat("z", 100)
	tail, truncated := tailResult(content, 16384)
	if truncated || tail != content {
		t.Fatalf("tailResult(100 bytes) = %d bytes, truncated=%t; want the input, untouched", len(tail), truncated)
	}

	dir := filepath.Join(t.TempDir(), "results")
	d := newTailDispatcher(t, 0, dir)
	got := backgroundResult(t, d, content)
	if got.Truncated || got.FullPath != "" || got.Content != content {
		t.Errorf("late result = %+v, want the untouched content", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("result dir exists after a small result: err=%v", err)
	}
}

func TestTailBoundaryEqualsMax(t *testing.T) {
	content := strings.Repeat("m", 16384)
	tail, truncated := tailResult(content, 16384)
	if truncated || tail != content {
		t.Errorf("content of exactly max bytes: truncated=%t, len=%d; want no cut", truncated, len(tail))
	}
}

func TestInlineResultNotCut(t *testing.T) {
	isolateHome(t)
	content := strings.Repeat("i", 20*1024)
	d := newDispatcher(nil, testSelf, nil)
	id := d.Fire(context.Background(), func(context.Context) (string, bool) {
		return content, false
	})
	out := d.Await(context.Background(), id, time.Second)
	if out.Background || out.Text != content {
		t.Errorf("inline result: background=%t, len=%d; want the full 20 KB", out.Background, len(out.Text))
	}
}

func TestFullFileWritten(t *testing.T) {
	isolateHome(t)
	dir := filepath.Join(t.TempDir(), "results")
	content := strings.Repeat("f", 40*1024)
	d := newTailDispatcher(t, 0, dir)
	got := backgroundResult(t, d, content)

	want := filepath.Join(dir, got.CallID+".txt")
	if got.FullPath != want {
		t.Fatalf("full_path = %q, want %q", got.FullPath, want)
	}
	if !got.Truncated {
		t.Error("truncated = false, want true")
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("file holds %d bytes, want the full %d", len(data), len(content))
	}
	if !strings.HasSuffix(got.Content, content[len(content)-16384:]) {
		t.Error("late content does not end with the last 16 KB")
	}
}

func TestFullFileWriteFailure(t *testing.T) {
	isolateHome(t)
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	d := newTailDispatcher(t, 0, filepath.Join(parent, "results"))
	got := backgroundResult(t, d, strings.Repeat("w", 20*1024))
	if !got.Truncated || got.FullPath != "" {
		t.Errorf("late result = truncated %t, full_path %q; want truncated and no path", got.Truncated, got.FullPath)
	}
	if !strings.Contains(got.Content, "full output could not be saved") {
		t.Errorf("content does not say the save failed: %q", got.Content[:min(len(got.Content), 200)])
	}
	if len(got.Content) < 16384 {
		t.Errorf("tail is %d bytes, want the 16 KB tail", len(got.Content))
	}
}

func TestConfigTailBytes(t *testing.T) {
	isolateHome(t)
	dir := filepath.Join(t.TempDir(), "results")
	d := newTailDispatcher(t, 1024, dir)
	got := backgroundResult(t, d, strings.Repeat("c", 4096))

	if !strings.Contains(got.Content, "[truncated: showing the last 1024 of 4096 bytes; full output: ") {
		t.Errorf("notice does not show the 1024 limit: %q", got.Content[:min(len(got.Content), 200)])
	}
	body := got.Content[strings.Index(got.Content, "\n")+1:]
	if len(body) != 1024 {
		t.Errorf("tail body = %d bytes, want 1024", len(body))
	}
}

func TestNoticeShowsTruncation(t *testing.T) {
	text := lateNotice(bus.ToolResult{CallID: "c3", DurationMS: 7, Truncated: true, FullPath: "/tmp/x/c3.txt"})
	want := `<tyci-notice kind=tool.result call_id=c3 failed=false duration_ms=7 truncated=true full_path="/tmp/x/c3.txt">`
	if !strings.HasPrefix(text, want) {
		t.Errorf("notice = %q, want prefix %q", text, want)
	}

	plain := lateNotice(bus.ToolResult{CallID: "c4"})
	if !strings.HasPrefix(plain, "<tyci-notice kind=tool.result call_id=c4 failed=false duration_ms=0 truncated=false>") {
		t.Errorf("plain notice = %q", plain)
	}
}

func TestToolResultDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := toolResultDir(nil); got != "" {
		t.Errorf("toolResultDir(nil) = %q, want empty", got)
	}

	sess, err := session.Open(filepath.Join(t.TempDir(), "s.jsonl"), "/tmp", "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	want := filepath.Join(home, ".tyci", "tool-results", sess.ID())
	if got := toolResultDir(sess); got != want {
		t.Errorf("toolResultDir = %q, want %q", got, want)
	}
}
