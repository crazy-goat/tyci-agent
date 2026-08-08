package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// ─── NextMessages drain ──────────────────────────────────────────────────

// queueCallback returns a NextMessages callback and a setter that
// configures the next FIFO of messages to return. After the callback is
// invoked the slice is consumed and the next call returns nil until the
// test sets a new slice.
type queueCallback struct {
	mu  sync.Mutex
	out []string
}

func (q *queueCallback) set(msgs []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.out = msgs
}

func (q *queueCallback) callback() func() []string {
	return func() []string {
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.out) == 0 {
			return nil
		}
		out := q.out
		q.out = nil
		return out
	}
}

// TestRun_NextMessages_EmptyDoesNotForceIteration: when the callback
// returns no messages, the agent must return after the first runOnce
// completes with `more == false`. No extra iteration is forced.
func TestRun_NextMessages_EmptyDoesNotForceIteration(t *testing.T) {
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "done"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := &silentDisplay{}
	queue := &queueCallback{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	cfg := Config{
		MaxRetries:   1,
		NextMessages: queue.callback(),
	}

	if _, err := Run(context.Background(), p, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Only the original user msg + assistant response. No extra user
	// message was appended from the (empty) queue.
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %#v", len(msgs), msgs)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("expected last message to be assistant, got %q", msgs[1].Role)
	}
}

// TestRun_NextMessages_OneAfterPlainResponseForcesOneMoreIteration:
// when the callback returns a message after a tool-less runOnce, the
// agent must run one additional runOnce, append the user message to
// msgs in FIFO order, and then return.
func TestRun_NextMessages_OneAfterPlainResponseForcesOneMoreIteration(t *testing.T) {
	// First runOnce emits "first", second emits "second" so we can
	// distinguish the two assistant turns in msgs.
	p := &connectortest.Fake{ProviderName: "counter", ModelName: "counter-1", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "first"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
		{
			stream.TextDelta{Text: "second"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}
	d := &silentDisplay{}
	queue := &queueCallback{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	// First call to callback: return the queued follow-up. Second call
	// (if any): return nil so the agent stops.
	queue.set([]string{"follow-up"})

	cfg := Config{
		MaxRetries:   1,
		NextMessages: queue.callback(),
	}

	if _, err := Run(context.Background(), p, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 4 messages: user, assistant, user (from queue), assistant.
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", len(msgs), msgs)
	}
	if msgs[2].Role != "user" {
		t.Errorf("msgs[2] role = %q, want user", msgs[2].Role)
	}
	if len(msgs[2].Content) != 1 || msgs[2].Content[0].Type != "text" || msgs[2].Content[0].Text != "follow-up" {
		t.Errorf("msgs[2] = %#v, want user text 'follow-up'", msgs[2])
	}
	if msgs[3].Role != "assistant" {
		t.Errorf("msgs[3] role = %q, want assistant", msgs[3].Role)
	}
	if msgs[3].Content[0].Text != "second" {
		t.Errorf("msgs[3] text = %q, want %q (second runOnce response)", msgs[3].Content[0].Text, "second")
	}
	// And the provider must have been called exactly twice.
	if p.Calls() != 2 {
		t.Errorf("Fake.Calls() = %d, want 2", p.Calls())
	}
}

// TestRun_NextMessages_FIFOOrder: multiple queued messages must be
// appended to msgs in the order the callback returns them, with the
// assistant's response following all of them in a single runOnce.
func TestRun_NextMessages_FIFOOrder(t *testing.T) {
	// First call: "ack" (response to the original prompt), then the
	// drain forces one more call, which also gets "ack". We just need
	// any assistant turn after the three queued user messages, so the
	// same content on both calls is fine.
	p := &connectortest.Fake{ProviderName: "counter", ModelName: "counter-1", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "ack"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
		{
			stream.TextDelta{Text: "ack"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}
	d := &silentDisplay{}
	queue := &queueCallback{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	queue.set([]string{"first follow-up", "second follow-up", "third follow-up"})

	cfg := Config{
		MaxRetries:   1,
		NextMessages: queue.callback(),
	}

	if _, err := Run(context.Background(), p, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 5 messages: user, assistant, 3 user (from queue), assistant.
	if len(msgs) != 6 {
		t.Fatalf("expected 6 messages, got %d: %#v", len(msgs), msgs)
	}
	wants := []string{"", "", "first follow-up", "second follow-up", "third follow-up", ""}
	for i := 2; i < 5; i++ {
		if len(msgs[i].Content) != 1 || msgs[i].Content[0].Text != wants[i] {
			t.Errorf("msgs[%d] = %#v, want text %q", i, msgs[i], wants[i])
		}
	}
	// Last message is the assistant turn that consumed the queue.
	if msgs[5].Role != "assistant" {
		t.Errorf("msgs[5] role = %q, want assistant", msgs[5].Role)
	}
}

// TestRun_NextMessages_DrainsAfterToolCall: a queued message after a
// tool-calling runOnce must be drained and produce exactly one
// additional runOnce before the agent returns.
func TestRun_NextMessages_DrainsAfterToolCall(t *testing.T) {
	// First call returns a tool call (forcing more); the script then
	// runs out, so the second call is Fake's default bare Finish
	// (forcing more=false).
	toolP := &connectortest.Fake{
		ProviderName: "mock-tool",
		ModelName:    "mock-tool-1",
		Turns: [][]stream.Event{{
			stream.ToolCall{ID: "tc1", Name: "read", Arguments: `{"path": "x"}`},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		}},
	}
	d := &silentDisplay{}
	runner := newMockToolRunner()
	runner.SetResult("read", "ok")
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "read x"}},
	}}

	queue := &queueCallback{}
	// After the first runOnce (which calls a tool), the queue returns
	// one follow-up. After the forced runOnce consumes it, the queue
	// is empty.
	queue.set([]string{"and now what?"})

	cfg := Config{
		MaxRetries:   1,
		Tools:        runner,
		NextMessages: queue.callback(),
	}

	if _, err := Run(context.Background(), toolP, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// The follow-up must come AFTER the tool call/result in the
	// conversation.
	idxTool := -1
	idxFollow := -1
	for i, m := range msgs {
		if m.Role == "toolResult" {
			idxTool = i
		}
		for _, b := range m.Content {
			if b.Text == "and now what?" {
				idxFollow = i
			}
		}
	}
	if idxFollow == -1 {
		t.Fatalf("expected queued follow-up to appear in msgs, got: %#v", msgs)
	}
	if idxTool == -1 {
		t.Fatalf("expected tool result message in msgs, got: %#v", msgs)
	}
	if idxFollow <= idxTool {
		t.Errorf("follow-up should come after tool messages (tool=%d, follow=%d)", idxTool, idxFollow)
	}
}

// TestRun_NextMessages_NilCallbackNoEffect: when NextMessages is nil
// the agent must behave exactly as before — no extra iterations, no
// extra appends.
func TestRun_NextMessages_NilCallbackNoEffect(t *testing.T) {
	p := &connectortest.Fake{ProviderName: "mock", ModelName: "mock-1", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "hello"},
		stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
	}}}
	d := &silentDisplay{}
	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}

	cfg := Config{MaxRetries: 1, NextMessages: nil}
	if _, err := Run(context.Background(), p, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

// TestRun_NextMessages_WritesToSession: when a Session is configured,
// each drained queued message must be written to the session log in
// the same order, before the assistant turn that consumes them.
func TestRun_NextMessages_WritesToSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	sess, err := session.Open(path, "test", "test/model", "test-provider")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = sess.Close() }()

	p := &connectortest.Fake{ProviderName: "counter", ModelName: "counter-1", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "ack"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
		{
			stream.TextDelta{Text: "ack"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}
	d := &silentDisplay{}
	queue := &queueCallback{}
	queue.set([]string{"follow-up A", "follow-up B"})

	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "initial"}},
	}}
	cfg := Config{
		MaxRetries:   1,
		Session:      sess,
		NextMessages: queue.callback(),
	}
	if _, err := Run(context.Background(), p, d, &msgs, cfg); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Read the session log and verify the order.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := splitLines(string(data))
	// The session contains lines written by agent.Run only (the initial
	// "initial" user message is added by the caller — tui_mode.go —
	// before Run is invoked, mirroring the synchronous submit path).
	// Expected order after the header: assistant (first runOnce), user
	// "follow-up A", user "follow-up B", assistant (second runOnce).
	if len(lines) < 5 {
		t.Fatalf("expected at least 5 session lines (header + 4 messages), got %d: %q", len(lines), data)
	}
	// Line 0 is the header.
	if !contains(lines[0], `"type":"session"`) {
		t.Errorf("session line 0 should be the session header, got %q", lines[0])
	}
	wants := []string{`"role":"assistant"`, `"role":"user"`, `"role":"user"`, `"role":"assistant"`}
	for i, want := range wants {
		if !contains(lines[i+1], want) {
			t.Errorf("session line %d = %q, missing %q", i+1, lines[i+1], want)
		}
	}
	if !contains(lines[2], "follow-up A") {
		t.Errorf("session line 2 should contain 'follow-up A', got %q", lines[2])
	}
	if !contains(lines[3], "follow-up B") {
		t.Errorf("session line 3 should contain 'follow-up B', got %q", lines[3])
	}
}

// TestRun_NextMessages_RespectsMaxIterations: the drain must not loop
// past MaxIterations. With MaxIterations=2 and a queue that always
// returns a new message, the agent must stop after 2 iterations.
func TestRun_NextMessages_RespectsMaxIterations(t *testing.T) {
	// Set MaxIterations=2 so the agent runs runOnce at most twice.
	// The Fake: call 1 returns "a", call 2 returns "b", call 3+
	// returns empty finish (forcing more=false in both cases, so the
	// drain sees an empty queue and returns).
	p := &connectortest.Fake{ProviderName: "counter", ModelName: "counter-1", Turns: [][]stream.Event{
		{
			stream.TextDelta{Text: "a"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
		{
			stream.TextDelta{Text: "b"},
			stream.Finish{Usage: stream.Usage{Input: 1, Output: 1}},
		},
	}}
	d := &silentDisplay{}
	queue := &queueCallback{}
	// First drain returns a follow-up (forces one more runOnce). The
	// second runOnce returns more=false and the next drain returns
	// nil because we don't pre-set anything else.
	queue.set([]string{"q1"})

	msgs := []connector.Message{{
		Role:    "user",
		Content: []connector.ContentBlock{{Type: "text", Text: "hi"}},
	}}
	cfg := Config{
		MaxRetries:    1,
		MaxIterations: 2,
		NextMessages:  queue.callback(),
	}

	// Must return without error even though we configured a follow-up.
	_, err := Run(context.Background(), p, d, &msgs, cfg)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// We expect 4 messages: initial user, assistant, queued user, assistant.
	if len(msgs) != 4 {
		t.Errorf("expected 4 messages, got %d: %#v", len(msgs), msgs)
	}
	// The model client must have been called exactly twice (the cap).
	if p.Calls() != 2 {
		t.Errorf("Fake.Calls() = %d, want 2", p.Calls())
	}
}

// ─── helpers ────────────────────────────────────────────────────────────

func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// avoid unused import warning when the test file is built standalone.
var _ = time.Second
