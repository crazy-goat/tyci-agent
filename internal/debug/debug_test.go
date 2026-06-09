package debug

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setHome sets HOME for the duration of the test.
func setHome(t *testing.T, dir string) {
	t.Helper()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
}

func TestInit_CreatesFileInCorrectDir(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	if l == nil {
		t.Fatal("Init() returned nil logger")
	}

	// Verify file exists
	expectedDir := filepath.Join(dir, ".tyci", "debug")
	entries, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("read debug dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in debug dir, got %d", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".log") {
		t.Errorf("expected .log file, got %q", entries[0].Name())
	}

	// Verify ID is non-empty and matches filename
	if l.ID == "" {
		t.Error("Logger.ID should not be empty")
	}
	expectedName := l.ID + ".log"
	if entries[0].Name() != expectedName {
		t.Errorf("expected file %q, got %q", expectedName, entries[0].Name())
	}
}

func TestInit_ErrorOnBadHome(t *testing.T) {
	// Point HOME to a path where ~/.tyci/debug cannot be created.
	// Using /dev/null as HOME means MkdirAll will fail because
	// /dev/null/.tyci/debug is not a valid path.
	if _, err := os.Stat("/dev/null"); err == nil {
		orig := os.Getenv("HOME")
		os.Setenv("HOME", "/dev/null")
		t.Cleanup(func() { os.Setenv("HOME", orig) })

		_, err := Init()
		if err == nil {
			t.Error("Init() expected error when HOME is /dev/null")
		}
	} else {
		t.Skip("skipping: /dev/null not available on this system")
	}
}

func TestLogger_Write(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	n, err := l.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if n != 11 {
		t.Errorf("Write() returned %d, want 11", n)
	}

	// Verify content
	l.Close()
	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("log content = %q, want %q", string(data), "hello world")
	}
}

func TestLogger_WriteAfterClose(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	l.Close()

	// Write after close should not panic and should return nil error
	n, err := l.Write([]byte("after close"))
	if err != nil {
		t.Fatalf("Write() after close error: %v", err)
	}
	if n != 11 {
		t.Errorf("Write() after close returned %d, want 11", n)
	}
}

func TestLogger_DoubleClose(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// First close should succeed
	l.Close()
	// Second close should not panic
	l.Close()
}

func TestLogger_WriteRequest(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	l.WriteRequest("POST", "https://api.example.com/v1/chat", []byte(`{"model":"gpt-4"}`))
	l.Close()

	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "--- REQUEST POST") {
		t.Errorf("expected REQUEST header, got %q", content)
	}
	if !strings.Contains(content, "https://api.example.com/v1/chat") {
		t.Errorf("expected URL in log, got %q", content)
	}
	if !strings.Contains(content, `{"model":"gpt-4"}`) {
		t.Errorf("expected body in log, got %q", content)
	}
}

func TestLogger_WriteResponse(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	l.WriteResponse(200, []byte(`{"id":"123"}`))
	l.Close()

	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "--- RESPONSE 200 ---") {
		t.Errorf("expected RESPONSE header, got %q", content)
	}
	if !strings.Contains(content, `{"id":"123"}`) {
		t.Errorf("expected body in log, got %q", content)
	}
}

func TestLogger_WriteRequestLine(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	l.WriteRequestLine("STREAM EVENT", []byte(`data: {"type":"text"}`))
	l.Close()

	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "--- STREAM EVENT ---") {
		t.Errorf("expected STREAM EVENT header, got %q", content)
	}
	if !strings.Contains(content, `data: {"type":"text"}`) {
		t.Errorf("expected body in log, got %q", content)
	}
}

func TestLogger_WriteResponseLine(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	l.WriteResponseLine([]byte(`some raw response line`))
	l.Close()

	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	content := string(data)

	if content != "some raw response line" {
		t.Errorf("log content = %q, want %q", content, "some raw response line")
	}
}

func TestNewContext_And_FromContext(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	ctx := NewContext(context.Background(), l)
	retrieved := FromContext(ctx)
	if retrieved == nil {
		t.Fatal("FromContext() returned nil")
	}
	if retrieved != l {
		t.Error("FromContext() did not return the same logger")
	}
}

func TestFromContext_NoLogger(t *testing.T) {
	ctx := context.Background()
	retrieved := FromContext(ctx)
	if retrieved != nil {
		t.Error("FromContext() should return nil when no logger in context")
	}
}

func TestFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), key{}, "not a logger")
	retrieved := FromContext(ctx)
	if retrieved != nil {
		t.Error("FromContext() should return nil when context value is wrong type")
	}
}

func TestNewUUIDv7_Format(t *testing.T) {
	id, err := newUUIDv7()
	if err != nil {
		t.Fatalf("newUUIDv7() error: %v", err)
	}

	// UUID v7 format: xxxxxxxx-xxxx-7xxx-8xxx-xxxxxxxxxxxx
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 parts, got %d: %q", len(parts), id)
	}

	// Check version nibble (should be 7 in the 3rd group)
	if len(parts[2]) > 0 {
		if parts[2][0] != '7' {
			t.Errorf("expected version nibble '7', got %q: %q", string(parts[2][0]), id)
		}
	}

	// Check variant nibble (should be 8/9/a/b in the 4th group)
	if len(parts[3]) > 0 {
		variant := parts[3][0]
		if variant != '8' && variant != '9' && variant != 'a' && variant != 'b' {
			t.Errorf("expected variant nibble 8/b, got %q: %q", string(variant), id)
		}
	}

	// Check total length
	if len(id) != 36 {
		t.Errorf("expected 36 chars, got %d: %q", len(id), id)
	}
}

func TestNewUUIDv7_Uniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newUUIDv7()
		if err != nil {
			t.Fatalf("newUUIDv7() error at iteration %d: %v", i, err)
		}
		if seen[id] {
			t.Fatalf("duplicate UUID at iteration %d: %q", i, id)
		}
		seen[id] = true
	}
}

func TestNewUUIDv7_TimestampOrdering(t *testing.T) {
	// Generate two UUIDs in quick succession — the first should have
	// a lexicographically smaller or equal timestamp prefix.
	id1, err := newUUIDv7()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := newUUIDv7()
	if err != nil {
		t.Fatal(err)
	}

	// Compare the timestamp part (first 12 hex chars)
	ts1 := id1[:8] + id1[9:13]
	ts2 := id2[:8] + id2[9:13]
	if ts2 < ts1 {
		t.Errorf("second UUID timestamp %q < first %q (should be >=)", ts2, ts1)
	}
}

func TestWriteRequest_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	// Should not panic with empty body
	l.WriteRequest("GET", "http://localhost", []byte{})
}

func TestWriteResponse_EmptyBody(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	l.WriteResponse(204, []byte{})
}

func TestConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	// Perform concurrent writes to test mutex safety
	const goroutines = 10
	done := make(chan bool, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			l.Write([]byte("line\n"))
			done <- true
		}(i)
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestInit_Permissions(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	path := filepath.Join(dir, ".tyci", "debug", l.ID+".log")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// File should be a regular file (not a directory)
	if !info.Mode().IsRegular() {
		t.Errorf("expected regular file, got %v", info.Mode())
	}

	// File should be non-empty after Init creates it
	if info.Size() != 0 {
		t.Errorf("expected empty file after init, got size %d", info.Size())
	}
}

func TestInit_DirPermissions(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	debugDir := filepath.Join(dir, ".tyci", "debug")
	info, err := os.Stat(debugDir)
	if err != nil {
		t.Fatalf("stat debug dir: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0755 {
		t.Errorf("debug dir permissions = %o, want 0755", perm)
	}
}

func TestWrite_ConcurrentWithClose(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}

	// Start goroutines that write, then close
	done := make(chan bool)
	go func() {
		for i := 0; i < 50; i++ {
			l.Write([]byte("data\n"))
		}
		done <- true
	}()
	go func() {
		for i := 0; i < 50; i++ {
			l.Write([]byte("more\n"))
		}
		done <- true
	}()
	go func() {
		l.Close()
		done <- true
	}()

	for i := 0; i < 3; i++ {
		<-done
	}
}

func TestWriteRequestLine_WithNewlines(t *testing.T) {
	dir := t.TempDir()
	setHome(t, dir)

	l, err := Init()
	if err != nil {
		t.Fatalf("Init() error: %v", err)
	}
	defer l.Close()

	// Body with embedded newlines
	body := []byte("line1\nline2\nline3")
	l.WriteRequestLine("MULTILINE", body)

	l.Close()
	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", l.ID+".log"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(data, []byte("line1\nline2\nline3")) {
		t.Errorf("expected multiline content, got %q", data)
	}
}
