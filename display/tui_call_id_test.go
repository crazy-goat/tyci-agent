package display

import (
	"testing"
	"time"
)

// TestDisplayEventsKeyedByCallID: parallel tools can end in any order. Each
// end must finish the block of its own call, not the block at the front of
// the queue.
func TestDisplayEventsKeyedByCallID(t *testing.T) {
	m := newTestModel()
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "read", callID: "t1"})
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-start", toolName: "grep", callID: "t2"})

	// t2 ends first.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "match", duration: time.Millisecond, callID: "t2"})

	if len(m.toolQueue) != 1 {
		t.Fatalf("queue length = %d, want 1 (only t1 still runs)", len(m.toolQueue))
	}
	left := m.blocks[m.toolQueue[0]]
	if left.callID != "t1" || left.toolState != "running" {
		t.Fatalf("remaining block = %+v, want running t1", left)
	}
	done := m.blocks[1]
	if done.callID != "t2" || done.toolState != "done" || done.output != "match" {
		t.Fatalf("finished block = %+v, want done t2 with its own output", done)
	}
	if m.blocks[0].toolState != "running" {
		t.Fatalf("t1 block state = %q, want running", m.blocks[0].toolState)
	}

	// An end without a known call does not touch a running block.
	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "x", callID: "t9"})
	if len(m.toolQueue) != 1 || m.blocks[0].toolState != "running" {
		t.Fatalf("an unknown call id changed the queue: %v", m.toolQueue)
	}

	m.handleBlockMsg(tuiMsgBlock{kind: "tool-end", content: "ok", callID: "t1"})
	if len(m.toolQueue) != 0 || m.blocks[0].toolState != "done" {
		t.Fatalf("t1 not finished: queue=%v state=%q", m.toolQueue, m.blocks[0].toolState)
	}
}
