package tools

import (
	"context"
	"strings"
	"testing"
)

func TestCappedBufferUnderCapIsExact(t *testing.T) {
	b := newCappedBuffer(1024, 1024)
	_, _ = b.Write([]byte("hello world"))
	if got := b.result(); got != "hello world" {
		t.Errorf("result = %q, want exact passthrough", got)
	}
	if b.overflowed {
		t.Error("small write should not overflow")
	}
}

func TestCappedBufferBoundsMemoryAndKeepsHeadTail(t *testing.T) {
	head, tail := 64, 64
	b := newCappedBuffer(head, tail)

	// Write far more than head+tail, in many chunks, to exercise the sliding
	// window and its compaction.
	for i := 0; i < 100000; i++ {
		_, _ = b.Write([]byte("0123456789")) // 1,000,000 bytes total
	}

	// Retained bytes must stay bounded regardless of total volume.
	if len(b.head) > head {
		t.Errorf("head len = %d, exceeds cap %d", len(b.head), head)
	}
	if len(b.tail) > tail {
		t.Errorf("tail len = %d, exceeds cap %d", len(b.tail), tail)
	}

	res := b.result()
	if !b.overflowed {
		t.Fatal("expected overflow")
	}
	if !strings.HasPrefix(res, "0123456789") {
		t.Errorf("result should start with the head, got %q...", res[:20])
	}
	if !strings.Contains(res, "truncated") {
		t.Error("truncation notice missing")
	}
	// The retained payload (head + tail) must be far smaller than total input.
	if len(b.head)+len(b.tail) > head+tail {
		t.Errorf("retained payload %d exceeds head+tail budget %d", len(b.head)+len(b.tail), head+tail)
	}
}

func TestCappedBufferHeadFillThenTail(t *testing.T) {
	b := newCappedBuffer(5, 5)
	_, _ = b.Write([]byte("AAAAA"))  // fills head exactly
	_, _ = b.Write([]byte("BBBBBB")) // overflows into tail
	if string(b.head) != "AAAAA" {
		t.Errorf("head = %q, want AAAAA", string(b.head))
	}
	if string(b.tail) != "BBBBB" {
		t.Errorf("tail = %q, want last 5 B's", string(b.tail))
	}
}

// TestBashToolCapsHugeOutput drives the real bash tool (buffered path) with a
// command that emits far more than the cap, and verifies the returned content
// is bounded.
func TestBashToolCapsHugeOutput(t *testing.T) {
	tool := &BashTool{}
	// ~5 MiB of output: yes prints forever; head -c bounds it deterministically.
	res := tool.Run(context.Background(), map[string]any{
		"command": "yes 0123456789 | head -c 5000000",
	})
	if !res.Success {
		t.Fatalf("bash failed: %s", res.Error)
	}
	if len(res.Content) > bashHeadMax+bashTailMax+1024 {
		t.Errorf("returned content len = %d, exceeds cap ~%d", len(res.Content), bashHeadMax+bashTailMax)
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Error("expected truncation notice in capped bash output")
	}
}
