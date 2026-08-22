package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decodo/tyci/stream"
)

func TestRepeatGuardAllowsOrdinaryOutput(t *testing.T) {
	var g repeatGuard
	text := `func main() {
	if a {
		return
	}
	if b {
		return
	}
}
`
	for _, line := range strings.SplitAfter(text, "\n") {
		if err := g.Feed(line); err != nil {
			t.Fatalf("real code tripped the guard: %v", err)
		}
	}
}

// TestRepeatGuardStopsARunawayLine reproduces what a real session did: a model
// spent 78 seconds emitting hundreds of copies of "</invoke>" and would have
// run to its token limit.
func TestRepeatGuardStopsARunawayLine(t *testing.T) {
	var g repeatGuard
	var err error
	for i := 0; i < maxRepeatedLines*4 && err == nil; i++ {
		err = g.Feed("</invoke>\n")
	}
	if err == nil {
		t.Fatal("the guard never fired")
	}
	if !strings.Contains(err.Error(), "</invoke>") {
		t.Errorf("the error should name the repeated line: %v", err)
	}
	if !strings.Contains(err.Error(), "repeated the same line") {
		t.Errorf("the error should say what happened: %v", err)
	}
}

// TestRepeatGuardIgnoresBlankLines: a stuck model often alternates its line
// with an empty one, and treating that as progress would defeat the check.
func TestRepeatGuardIgnoresBlankLines(t *testing.T) {
	var g repeatGuard
	var err error
	for i := 0; i < maxRepeatedLines*4 && err == nil; i++ {
		err = g.Feed("</invoke>\n\n")
	}
	if err == nil {
		t.Fatal("blank lines between repeats hid the loop")
	}
}

// TestRepeatGuardWorksAcrossDeltaBoundaries: deltas do not arrive split on
// newlines, so lines have to be assembled rather than assumed.
func TestRepeatGuardWorksAcrossDeltaBoundaries(t *testing.T) {
	var g repeatGuard
	var err error
	for i := 0; i < maxRepeatedLines*4 && err == nil; i++ {
		for _, piece := range []string{"</in", "voke", ">", "\n"} {
			if err = g.Feed(piece); err != nil {
				break
			}
		}
	}
	if err == nil {
		t.Fatal("a loop split across deltas went undetected")
	}
}

// TestRepeatGuardLeavesLongIdenticalLinesAlone: a long line repeated many
// times is far more likely to be real content — a generated fixture, a data
// table — than a stuck decoder.
func TestRepeatGuardLeavesLongIdenticalLinesAlone(t *testing.T) {
	var g repeatGuard
	long := strings.Repeat("x", maxRepeatedLineLen+1) + "\n"
	for i := 0; i < maxRepeatedLines*3; i++ {
		if err := g.Feed(long); err != nil {
			t.Fatalf("long content tripped the guard: %v", err)
		}
	}
}

func TestRepeatGuardResetsOnProgress(t *testing.T) {
	var g repeatGuard
	for i := 0; i < maxRepeatedLines*3; i++ {
		if err := g.Feed("same\n"); err != nil {
			// Interrupt the run with a different line before the cap.
			t.Fatalf("fired too early at %d: %v", i, err)
		}
		if err := g.Feed("different\n"); err != nil {
			t.Fatalf("alternating lines tripped the guard: %v", err)
		}
	}
}

// TestChatStreamAbortsARepeatingModel is the end-to-end effect: the request is
// cut off, so the agent can fall back to another model instead of paying for a
// loop to reach its token limit.
func TestChatStreamAbortsARepeatingModel(t *testing.T) {
	var sse strings.Builder
	for i := 0; i < maxRepeatedLines*3; i++ {
		sse.WriteString(`data: {"choices":[{"delta":{"reasoning_content":"</invoke>\n"}}]}` + "\n\n")
	}
	sse.WriteString("data: [DONE]\n")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(sse.String()))
	}))
	defer server.Close()

	emitted := 0
	emit := func(e stream.Event) error {
		if _, ok := e.(stream.ThinkingDelta); ok {
			emitted++
		}
		return nil
	}

	body := ChatRequest{Model: "glm", Stream: true, Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
	err := (ChatStreamer{}).Stream(testCtx(), "k", server.URL, body, emit)
	if err == nil {
		t.Fatal("the stream ran to completion; a loop must be cut off")
	}
	if !strings.Contains(err.Error(), "stopped making progress") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Cut off early, not after the whole loop had been streamed.
	if emitted > maxRepeatedLines+2 {
		t.Errorf("emitted %d deltas before stopping, expected about %d", emitted, maxRepeatedLines)
	}
	// A plain error, not a retryable one: repeating the same request would
	// reproduce the same loop. The agent falls back to another model instead.
	if IsRetryable(err) {
		t.Error("a stuck model should not be retried with the same request")
	}
}
