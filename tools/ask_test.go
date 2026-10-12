package tools

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeJobAsker is a JobAsker that returns a fixed question id, or ok=false.
type fakeJobAsker struct {
	qid string
	ok  bool
}

func (f fakeJobAsker) Ask(ctx context.Context, id, question string) (string, bool) {
	return f.qid, f.ok
}

// fakeJobAnswerer records the id and text it was called with and returns err.
type fakeJobAnswerer struct {
	gotID   string
	gotText string
	err     error
}

func (f *fakeJobAnswerer) Answer(ctx context.Context, id, text string) error {
	f.gotID, f.gotText = id, text
	return f.err
}

func TestAskReturnsImmediately(t *testing.T) {
	SetJobAsker(fakeJobAsker{qid: "q7", ok: true})
	t.Cleanup(func() { SetJobAsker(nil) })

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	start := time.Now()
	res := (&AskTool{}).Run(ctx, map[string]any{"question": "which branch?"})
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("ask_parent failed: %s", res.Error)
	}
	if elapsed > 50*time.Millisecond {
		t.Fatalf("ask_parent took %s, want it to return at once", elapsed)
	}
	if !strings.Contains(res.Content, "id=q7") {
		t.Fatalf("content %q does not carry the question id q7", res.Content)
	}
	if !strings.Contains(res.Content, `wait(kinds=["ask.reply"])`) {
		t.Fatalf("content %q does not name the ask.reply wait", res.Content)
	}
}

func TestAskFailsWhenNotPublished(t *testing.T) {
	SetJobAsker(fakeJobAsker{ok: false})
	t.Cleanup(func() { SetJobAsker(nil) })

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	res := (&AskTool{}).Run(ctx, map[string]any{"question": "which branch?"})
	if res.Success {
		t.Fatal("ask_parent succeeded although the question was not published")
	}
}

func TestAnswerToolReturnsAnswererError(t *testing.T) {
	answerer := &fakeJobAnswerer{err: errors.New("unknown question id")}
	SetJobAnswerer(answerer)
	t.Cleanup(func() { SetJobAnswerer(nil) })

	res := (&AnswerTool{}).Run(context.Background(), map[string]any{"job_id": "q9", "text": "main"})
	if res.Success {
		t.Fatal("answer_job succeeded although Answer returned an error")
	}
	if res.Error != "unknown question id" {
		t.Fatalf("error = %q, want the answerer error", res.Error)
	}
	if answerer.gotID != "q9" || answerer.gotText != "main" {
		t.Fatalf("Answer got (%q, %q), want (q9, main)", answerer.gotID, answerer.gotText)
	}
}

func TestWaitKindsAskReply(t *testing.T) {
	if msg := validateWaitKinds([]string{"ask.reply"}); msg != "" {
		t.Fatalf("ask.reply rejected: %s", msg)
	}
	res := (&WaitTool{}).Run(context.Background(), map[string]any{"seconds": 1, "kinds": []any{"agent.message"}})
	if res.Success {
		t.Fatal("wait accepted an unsupported kind")
	}
	if _, errText := waitKinds(map[string]any{"kinds": []any{1}}); errText == "" {
		t.Fatal("wait accepted a non-string kind")
	}
}

// postingMailbox reports a new notice for the caller once n is raised.
type postingMailbox struct {
	raceJobMailbox
	n atomic.Uint64
}

func (m *postingMailbox) Posted(id string) uint64 { return m.n.Load() }

// TestWaitKindsAloneWaitsForReply calls wait(kinds=["ask.reply"]) with no
// seconds and no job id. It must not fail on the missing seconds, and it must
// return as soon as the reply notice arrives.
func TestWaitKindsAloneWaitsForReply(t *testing.T) {
	mb := &postingMailbox{}
	SetJobMailbox(mb)
	t.Cleanup(func() { SetJobMailbox(nil) })
	go func() {
		time.Sleep(50 * time.Millisecond)
		mb.n.Add(1)
	}()

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-1-1")
	start := time.Now()
	res := (&WaitTool{}).Run(ctx, map[string]any{"kinds": []any{"ask.reply"}})
	if !res.Success {
		t.Fatalf("wait(kinds=[ask.reply]) failed: %s", res.Error)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("wait did not stop when the reply arrived (took %s)", time.Since(start))
	}
	if !strings.Contains(res.Content, "notice arrived") {
		t.Fatalf("content %q does not report the arrived notice", res.Content)
	}
}
