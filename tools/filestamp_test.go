package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests deliberately create their fixtures with os.WriteFile rather
// than the writeFile helper: the helper stamps the file as read, which is
// exactly the state under test here.

// touchLater rewrites path with new content and a mtime the guard can tell
// apart. Filesystem timestamp granularity varies (HFS+ was 1s), so the mtime
// is set explicitly instead of hoping the clock moved.
func touchLater(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestWriteGuardAllowsNewFile(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "new.txt")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "content": "hello",
	})
	if !res.Success {
		t.Fatalf("creating a file must not need a prior read: %s", res.Error)
	}
}

// TestWriteGuardBlocksOverwriteOfUnreadFile is the blind-clobber case: the
// agent has never seen this file, so it cannot know what it is destroying.
func TestWriteGuardBlocksOverwriteOfUnreadFile(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("important\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "content": "clobbered",
	})
	if res.Success {
		t.Fatal("overwrote a file the agent never read")
	}
	if !strings.Contains(res.Error, "have not read it") {
		t.Fatalf("error should say what to do next, got %q", res.Error)
	}
	// The point of refusing is that the bytes survive.
	if data, _ := os.ReadFile(path); string(data) != "important\n" {
		t.Fatalf("file was modified anyway: %q", string(data))
	}
}

// TestWriteGuardAllowsWriteAfterRead is the ordinary happy path, driven
// through the read tool so it covers the actual wiring and not just the
// bookkeeping helper.
func TestWriteGuardAllowsWriteAfterRead(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path}); !res.Success {
		t.Fatalf("read failed: %s", res.Error)
	}
	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "two", "newString": "TWO",
	})
	if !res.Success {
		t.Fatalf("edit after read must be allowed: %s", res.Error)
	}
}

// TestWriteGuardBlocksWriteAfterExternalChange is the bug this whole file
// exists for: a human saves the file in their editor between the agent's read
// and its write, and the write would silently discard their work.
func TestWriteGuardBlocksWriteAfterExternalChange(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path}); !res.Success {
		t.Fatalf("read failed: %s", res.Error)
	}

	touchLater(t, path, "one\ntwo\nthree the human added\n")

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "two", "newString": "TWO",
	})
	if res.Success {
		t.Fatal("wrote over a file that changed after the read")
	}
	if !strings.Contains(res.Error, "changed on disk") {
		t.Fatalf("error should name the cause, got %q", res.Error)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "three the human added") {
		t.Fatalf("the human's line was lost: %q", string(data))
	}
}

// TestWriteGuardReReadClearsTheBlock: the error tells the model to read the
// file again, so doing that has to actually unblock the write.
func TestWriteGuardReReadClearsTheBlock(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path}); !res.Success {
		t.Fatalf("read failed: %s", res.Error)
	}
	touchLater(t, path, "one\ntwo\n")

	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path}); !res.Success {
		t.Fatalf("re-read failed: %s", res.Error)
	}
	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "two", "newString": "TWO",
	})
	if !res.Success {
		t.Fatalf("re-reading must clear the block: %s", res.Error)
	}
}

// TestWriteGuardAllowsConsecutiveEdits: our own write refreshes the record,
// so a multi-edit turn does not have to re-read between every change.
func TestWriteGuardAllowsConsecutiveEdits(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": path}); !res.Success {
		t.Fatalf("read failed: %s", res.Error)
	}

	tool := &WriteTool{}
	for _, edit := range [][2]string{{"a", "A"}, {"b", "B"}, {"c", "C"}} {
		res := tool.Run(context.Background(), map[string]any{
			"path": path, "oldString": edit[0], "newString": edit[1],
		})
		if !res.Success {
			t.Fatalf("edit %q->%q failed: %s", edit[0], edit[1], res.Error)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "A\nB\nC\n" {
		t.Fatalf("got %q", string(data))
	}
}

// TestWriteGuardExemptsAppend: appending destroys nothing and addresses no
// line numbers, so requiring a read first would be friction with no payoff.
func TestWriteGuardExemptsAppend(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "log.txt")
	if err := os.WriteFile(path, []byte("existing\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "content": "appended\n", "range": "append",
	})
	if !res.Success {
		t.Fatalf("append to an unread file must be allowed: %s", res.Error)
	}
	if data, _ := os.ReadFile(path); string(data) != "existing\nappended\n" {
		t.Fatalf("got %q", string(data))
	}
}

// TestWriteGuardAppendDoesNotGrantOverwrite: appending to a file the agent
// never read must not be a back door to overwriting it.
func TestWriteGuardAppendDoesNotGrantOverwrite(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "log.txt")
	if err := os.WriteFile(path, []byte("existing\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &WriteTool{}
	if res := tool.Run(context.Background(), map[string]any{
		"path": path, "content": "appended\n", "range": "append",
	}); !res.Success {
		t.Fatalf("append failed: %s", res.Error)
	}
	res := tool.Run(context.Background(), map[string]any{"path": path, "content": "clobbered"})
	if res.Success {
		t.Fatal("append granted overwrite permission on an unread file")
	}
}

// TestWriteGuardBlocksStaleDryRun: a dry run reports line numbers, and
// reporting them from a file the agent has not seen is worse than refusing —
// the model would act on them.
func TestWriteGuardBlocksStaleDryRun(t *testing.T) {
	ResetFileStamps()
	path := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(path, []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": path, "oldString": "target", "newString": "changed", "dryRun": true,
	})
	if res.Success {
		t.Fatal("dry run reported positions in a file the agent never read")
	}
}

// TestWriteGuardNormalisesPaths: "./x.txt" and an absolute path are the same
// file, and the guard must not treat a read of one as unrelated to a write of
// the other.
func TestWriteGuardNormalisesPaths(t *testing.T) {
	ResetFileStamps()
	dir := t.TempDir()
	abs := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(abs, []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })

	if res := (&ReadTool{}).Run(context.Background(), map[string]any{"path": "./x.txt"}); !res.Success {
		t.Fatalf("read failed: %s", res.Error)
	}
	res := (&WriteTool{}).Run(context.Background(), map[string]any{
		"path": abs, "oldString": "one", "newString": "ONE",
	})
	if !res.Success {
		t.Fatalf("relative read should satisfy an absolute write: %s", res.Error)
	}
}

// TestFileStampsAreBounded: the map must not grow for the lifetime of a long
// session. Dropping stamps fails closed — the next write asks for a re-read —
// so the only thing to verify is that it stays bounded.
func TestFileStampsAreBounded(t *testing.T) {
	ResetFileStamps()
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// recordFileStamp stats the path, so every key needs a real file; one
	// file under many distinct relative names is enough to fill the map.
	for i := 0; i < maxFileStamps+50; i++ {
		nested := filepath.Join(dir, strings.Repeat("./", i%3), "x.txt")
		recordFileStamp(nested)
		fileStamps.mu.Lock()
		n := len(fileStamps.m)
		fileStamps.mu.Unlock()
		if n > maxFileStamps {
			t.Fatalf("map grew to %d entries, cap is %d", n, maxFileStamps)
		}
	}
}
