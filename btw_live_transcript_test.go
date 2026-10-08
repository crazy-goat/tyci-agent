package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// liveTextOf joins the text events of a live transcript.
func liveTextOf(events []tools.LiveEvent) string {
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind == "text" {
			b.WriteString(ev.Content)
		}
	}
	return b.String()
}

// TestJobResumerAdapter_ResumedJobHasLiveTranscript covers a row that does not
// run through runSingleTask. Enter on a resumed job's row must open its agent
// view, so the resumed job needs a live transcript. The view starts with the
// turns of the job it continues.
func TestJobResumerAdapter_ResumedJobHasLiveTranscript(t *testing.T) {
	reg, _ := withTestWiring(t)
	resetResumableForTest(t)

	const origJobID = "resume-live-orig"
	tools.RecordLiveEvent(origJobID, tools.LiveEvent{Kind: "text", Content: "earlier turn"})
	fake := connectortest.Text("resumed answer")
	stashResumable(origJobID, resumableEntry{
		msgs: []connector.Message{
			{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "hi"}}},
		},
		mc:          fake,
		cfg:         agent.Config{MaxRetries: 1},
		todoAgentID: origJobID,
	})

	ctx := connector.WithModelClient(context.Background(), fake)
	handle, err := (jobResumerAdapter{reg: reg}).Resume(ctx, origJobID, "continue")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, ok := reg.Wait(context.Background(), handle.ID(), 5*time.Second); !ok {
		t.Fatal("resumed job never finished")
	}

	live, ok := tools.LiveTranscriptSince(handle.ID(), 0)
	if !ok || len(live) < 2 || live[0].Content != "earlier turn" || !strings.Contains(liveTextOf(live), "resumed answer") {
		t.Fatalf("resumed job live transcript = %+v (ok=%v), want the earlier turn then the resumed answer", live, ok)
	}
}

// TestStartBtw_EvaluationHasLiveTranscript covers the /btw evaluation path.
// Enter on the evaluation's row must open its agent view, so the evaluation
// records its conversation in the live transcript. The sink must still get
// the text, because it is the job result.
func TestStartBtw_EvaluationHasLiveTranscript(t *testing.T) {
	reg := jobs.NewRegistry()
	prevRegistry, prevNotices := JobRegistry, JobNotices
	JobRegistry, JobNotices = reg, jobs.NewNotifier()
	defer func() { JobRegistry, JobNotices = prevRegistry, prevNotices }()

	fake := connectortest.Text("btw answer")
	cond := conductor.New(conductor.Options{Client: fake, Sink: noopDisplay{}})
	sink := display.NewBtwSink(&display.TUI{}, "btw-live-test")

	job := startBtw(context.Background(), cond, "a side question", sink)
	if job == nil {
		t.Fatal("startBtw refused the evaluation")
	}
	defer func() {
		btwEvaluationsMu.Lock()
		delete(btwEvaluations, job.ID)
		btwEvaluationsMu.Unlock()
	}()
	if _, ok := reg.Wait(context.Background(), job.ID, 5*time.Second); !ok {
		t.Fatal("btw evaluation never finished")
	}

	live, ok := tools.LiveTranscriptSince(job.ID, 0)
	if !ok || !strings.Contains(liveTextOf(live), "btw answer") {
		t.Fatalf("evaluation live transcript = %+v (ok=%v), want the answer", live, ok)
	}
	if got := sink.CollectedText(); got != "btw answer" {
		t.Fatalf("sink collected %q, want the answer", got)
	}
}
