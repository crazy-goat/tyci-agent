package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

type fakeManager struct {
	req  flow.StartRequest
	sub  func(flow.RunEvent)
	text string
}

func (m *fakeManager) Start(_ context.Context, req flow.StartRequest) (string, []string, error) {
	m.req = req
	return "20260101-000000-1", nil, nil
}

func (m *fakeManager) RunText(_ context.Context, _, input string) (string, error) {
	m.text = input
	return "answer", nil
}

func (m *fakeManager) Subscribe(fn func(flow.RunEvent)) func() {
	m.sub = fn
	return func() {}
}

func TestFlowRunnerAskResumeMerged(t *testing.T) {
	m := &fakeManager{}
	r := NewRunner(m)
	h, err := r.Start(context.Background(), "issue-to-merge", map[string]string{"issue": "7"})
	if err != nil || m.req.Issue != 7 || m.req.Workflow != "issue-to-merge" {
		t.Fatalf("err=%v req=%+v", err, m.req)
	}
	m.sub(flow.RunEvent{Run: "other", Status: "failed"})
	m.sub(flow.RunEvent{Run: h.ID(), Status: "paused"})
	select {
	case <-h.Asks():
	case <-time.After(wait):
		t.Fatal("no ask")
	}
	m.sub(flow.RunEvent{Run: h.ID(), Status: "running"})
	select {
	case <-h.Resumed():
	case <-time.After(wait):
		t.Fatal("no resume")
	}
	m.sub(flow.RunEvent{Run: h.ID(), Status: "done", PR: 9})
	select {
	case res := <-h.Done():
		if res.Outcome != "merged" {
			t.Fatalf("%+v", res)
		}
	case <-time.After(wait):
		t.Fatal("no result")
	}
}

func TestFlowRunnerTextInput(t *testing.T) {
	m := &fakeManager{}
	h, err := NewRunner(m).Start(context.Background(), "roadmap", map[string]string{"input": "{}"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-h.Done():
		if res.Outcome != "ended" || res.Output != "answer" || m.text != "{}" {
			t.Fatalf("%+v text=%q", res, m.text)
		}
	case <-time.After(wait):
		t.Fatal("no result")
	}
}

func TestFlowRunnerRejectsOtherInputs(t *testing.T) {
	r := NewRunner(&fakeManager{})
	if _, err := r.Start(context.Background(), "roadmap", map[string]string{"x": "{}"}); err == nil {
		t.Fatal("want error")
	}
}
