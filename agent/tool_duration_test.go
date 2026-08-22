package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// durationRunner sleeps for calls named "slow" and returns at once otherwise.
type durationRunner struct{}

func (durationRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "bash" {
		time.Sleep(150 * time.Millisecond)
	}
	return "ok", nil
}

// TestExecuteToolsTimesEachCallSeparately is the reported bug: a batch of four
// tools showed the same figure on every row. The display timed from
// ToolCallStart — emitted for the whole batch before any of it runs — to
// ToolCallEnd, emitted after all of it finishes, so every row got the batch's
// wall-clock. The dispatcher is the only place that can see the real figure.
func TestExecuteToolsTimesEachCallSeparately(t *testing.T) {
	calls := []stream.ToolCall{
		{ID: "1", Name: "read", Arguments: "{}"},
		{ID: "2", Name: "bash", Arguments: "{}"},
		{ID: "3", Name: "read", Arguments: "{}"},
	}

	results, durations := executeTools(context.Background(), durationRunner{}, calls)
	if len(results) != 3 || len(durations) != 3 {
		t.Fatalf("got %d results and %d durations", len(results), len(durations))
	}
	if durations[1] < 100*time.Millisecond {
		t.Errorf("the slow call was timed at %v, expected at least 100ms", durations[1])
	}
	for _, i := range []int{0, 2} {
		if durations[i] > 80*time.Millisecond {
			t.Errorf("quick call %d timed at %v — that is the batch, not the call", i, durations[i])
		}
	}
}

// recordingSink captures the durations reported alongside each tool-end, in
// order, the way the TUI consumes them.
type recordingSink struct {
	ends      []string
	durations []time.Duration
}

func (s *recordingSink) Request(string)                          {}
func (s *recordingSink) Thinking(string)                         {}
func (s *recordingSink) Text(string)                             {}
func (s *recordingSink) ToolCallStart(string)                    {}
func (s *recordingSink) ToolCallDelta(string)                    {}
func (s *recordingSink) ToolFinish()                             {}
func (s *recordingSink) ToolBlock(string)                        {}
func (s *recordingSink) Summary(u stream.Usage, st stream.Stats) {}
func (s *recordingSink) Total(stream.Usage)                      {}
func (s *recordingSink) Error(error)                             {}
func (s *recordingSink) End()                                    {}
func (s *recordingSink) ToolCallDuration(d time.Duration)        { s.durations = append(s.durations, d) }
func (s *recordingSink) ToolCallEnd(name, result string)         { s.ends = append(s.ends, name) }

// TestDurationsReachTheDisplayPairedWithTheirTool: measuring is only half of
// it. Each figure has to arrive next to the tool it belongs to, in the order
// the display pops its queue.
func TestDurationsReachTheDisplayPairedWithTheirTool(t *testing.T) {
	calls := []stream.ToolCall{
		{ID: "1", Name: "read", Arguments: "{}"},
		{ID: "2", Name: "bash", Arguments: "{}"},
	}
	durations := []time.Duration{2 * time.Millisecond, 4300 * time.Millisecond}

	sink := &recordingSink{}
	var msgs []connector.Message
	appendToolResults(sink, &msgs, Config{}, calls, []string{"a", "b"}, durations)

	if len(sink.durations) != 2 {
		t.Fatalf("reported %d durations, want 2", len(sink.durations))
	}
	if sink.durations[0] != durations[0] || sink.durations[1] != durations[1] {
		t.Fatalf("durations arrived as %v, want %v", sink.durations, durations)
	}
	if len(sink.ends) != 2 || sink.ends[0] != "read" || sink.ends[1] != "bash" {
		t.Fatalf("tool ends arrived as %v", sink.ends)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected two tool-result messages, got %d", len(msgs))
	}
}

// blockRecordingSink captures the notices a display would show the user.
type blockRecordingSink struct {
	recordingSink
	blocks []string
}

func (s *blockRecordingSink) ToolBlock(msg string) { s.blocks = append(s.blocks, msg) }

// TestTruncatedReplyIsReported: finish_reason "length" was parsed and then
// dropped, so a reply that stopped mid-sentence looked like a terse one, and a
// truncated tool call surfaced as "invalid arguments" — sending the model to
// look for a bug in JSON it had written correctly and simply not finished.
func TestTruncatedReplyIsReported(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "half an ans"},
		stream.Finish{Reason: "length"},
	}}}

	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var usage stream.Usage
	if _, _, _, err := runOnce(context.Background(), mc, sink, &msgs, Config{MaxTokens: 4096}, &usage); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(sink.blocks, "\n")
	if !strings.Contains(joined, "cut off") {
		t.Fatalf("nothing told the user the reply was truncated: %v", sink.blocks)
	}
	if !strings.Contains(joined, "4096") {
		t.Errorf("the notice should name the limit in force: %q", joined)
	}
	if !strings.Contains(joined, "max-tokens") {
		t.Errorf("the notice should say how to change it: %q", joined)
	}
}

func TestNormalFinishIsNotReported(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{{
		stream.TextDelta{Text: "a complete answer"},
		stream.Finish{Reason: "stop"},
	}}}

	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}}}
	var usage stream.Usage
	if _, _, _, err := runOnce(context.Background(), mc, sink, &msgs, Config{}, &usage); err != nil {
		t.Fatal(err)
	}
	for _, b := range sink.blocks {
		if strings.Contains(b, "cut off") {
			t.Fatalf("a normal finish was reported as truncated: %q", b)
		}
	}
}

// TestBlockedJobStopsTheTurnEnding is the most expensive mistake this
// environment makes possible: the model ends its turn while a child sits
// blocked on a question. The child makes no progress and everything it did is
// discarded when its wall clock runs out, and the only thing that could have
// saved it was one answer() call from the turn that just ended.
func TestBlockedJobStopsTheTurnEnding(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{stream.TextDelta{Text: "all done"}, stream.Finish{Reason: "stop"}},
		{stream.TextDelta{Text: "answering it now"}, stream.Finish{Reason: "stop"}},
	}}

	asked := 0
	cfg := Config{
		MaxRetries: 1,
		PendingJobs: func() []string {
			asked++
			if asked == 1 {
				return []string{`WAITING FOR ANSWER: review the auth flow (job_id=job-7) asks: "which branch?"`}
			}
			return nil
		},
	}

	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), mc, sink, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	var reminder string
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "automated check") {
				reminder = b.Text
			}
		}
	}
	if reminder == "" {
		t.Fatal("the turn ended with a blocked child and nothing said so")
	}
	for _, want := range []string{"WAITING FOR ANSWER", "job-7", "which branch?", "answer(job_id="} {
		if !strings.Contains(reminder, want) {
			t.Errorf("the reminder is missing %q:\n%s", want, reminder)
		}
	}
	// Framed as the harness, not as the user: the user did not say this.
	if !strings.Contains(reminder, "not the user") {
		t.Errorf("the reminder must not read as a user message:\n%s", reminder)
	}
}

// TestNoJobsMeansNoReminder: the nudge must not fire on an ordinary turn.
func TestNoJobsMeansNoReminder(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{stream.TextDelta{Text: "done"}, stream.Finish{Reason: "stop"}},
	}}

	cfg := Config{MaxRetries: 1, PendingJobs: func() []string { return nil }}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), mc, sink, &msgs, cfg); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "automated check") {
				t.Fatalf("a reminder was injected with no jobs outstanding: %q", b.Text)
			}
		}
	}
}

// TestJobReminderIsBounded: a model that ignores the nudge must not be nagged
// in a loop.
func TestJobReminderIsBounded(t *testing.T) {
	sink := &blockRecordingSink{}
	turns := make([][]stream.Event, 0, 8)
	for i := 0; i < 8; i++ {
		turns = append(turns, []stream.Event{stream.TextDelta{Text: "done"}, stream.Finish{Reason: "stop"}})
	}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: turns}

	cfg := Config{
		MaxRetries:  1,
		PendingJobs: func() []string { return []string{"running: forever (job_id=job-1)"} },
	}
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), mc, sink, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	count := 0
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "automated check") {
				count++
			}
		}
	}
	if count > maxJobReminders {
		t.Fatalf("nagged %d times, cap is %d", count, maxJobReminders)
	}
}

// TestInterruptNoteExplainsNothingWasCancelled. A person can type while work is
// running, and a tool that can hand its work to the background does so at once
// — so the model arrives mid-task with a question out of nowhere. Without this
// note it reads that as a change of instructions and abandons work that is
// perfectly fine.
func TestInterruptNoteExplainsNothingWasCancelled(t *testing.T) {
	note := buildInterruptNote([]string{
		"running: review the auth flow (job_id=job-9)",
	})

	for _, want := range []string{
		"automated note, not the user",
		"Nothing was cancelled",
		"job-9",
		"Answer them first",
		"notified as each finishes",
		"wait(job_id=",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the note is missing %q:\n%s", want, note)
		}
	}
}

// TestQueuedMessageCarriesTheInterruptNote wires it to the loop: the note has
// to arrive with the user's line, not on some later turn.
func TestQueuedMessageCarriesTheInterruptNote(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{stream.TextDelta{Text: "working"}, stream.Finish{Reason: "stop"}},
		{stream.TextDelta{Text: "answering you"}, stream.Finish{Reason: "stop"}},
	}}

	delivered := false
	cfg := Config{
		MaxRetries: 1,
		NextMessages: func() []string {
			if delivered {
				return nil
			}
			delivered = true
			return []string{"actually, what about the tests?"}
		},
		PendingJobs: func() []string { return []string{"running: the long one (job_id=job-1)"} },
	}

	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), mc, sink, &msgs, cfg); err != nil {
		t.Fatal(err)
	}

	var userLineIdx, noteIdx = -1, -1
	for i, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "what about the tests?") {
				userLineIdx = i
			}
			if strings.Contains(b.Text, "Nothing was cancelled") {
				noteIdx = i
			}
		}
	}
	if userLineIdx < 0 {
		t.Fatal("the queued line was not delivered")
	}
	if noteIdx < 0 {
		t.Fatal("no interrupt note accompanied it")
	}
	// After the line, so the model reads the question first and the context
	// second — the other order buries what the person actually said.
	if noteIdx < userLineIdx {
		t.Errorf("the note came before the user's line (%d < %d)", noteIdx, userLineIdx)
	}
}

// TestNoInterruptNoteWithNothingRunning: an ordinary follow-up must arrive
// clean, with no harness prose attached.
func TestNoInterruptNoteWithNothingRunning(t *testing.T) {
	sink := &blockRecordingSink{}
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{stream.TextDelta{Text: "a"}, stream.Finish{Reason: "stop"}},
		{stream.TextDelta{Text: "b"}, stream.Finish{Reason: "stop"}},
	}}

	delivered := false
	cfg := Config{
		MaxRetries: 1,
		NextMessages: func() []string {
			if delivered {
				return nil
			}
			delivered = true
			return []string{"one more thing"}
		},
		PendingJobs: func() []string { return nil },
	}

	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go"}}}}
	if _, err := Run(context.Background(), mc, sink, &msgs, cfg); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, "Nothing was cancelled") {
				t.Fatal("an interrupt note was attached with nothing running")
			}
		}
	}
}
