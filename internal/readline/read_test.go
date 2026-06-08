package readline

import (
	"os"
	"testing"
)

func TestHasMoreData(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	defer w.Close()

	e := &LineEditor{fd: int(r.Fd())}

	// No data written — should return false.
	if e.hasMoreData() {
		t.Error("hasMoreData() = true, want false (no data)")
	}

	// Write a byte — should return true.
	if _, err := w.Write([]byte{'x'}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !e.hasMoreData() {
		t.Error("hasMoreData() = false, want true (data available)")
	}

	// Drain the byte.
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	// After draining — should return false again.
	if e.hasMoreData() {
		t.Error("hasMoreData() = true, want false (drained)")
	}
}
