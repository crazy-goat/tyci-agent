package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

type fakeManager struct {
	req   flow.StartRequest
	state chan *flow.RunState
	last  *flow.RunState
}

func (m *fakeManager) Start(_ context.Context, req flow.StartRequest) (string, []string, error) {
	m.req = req
	return "20260101-000000-1", nil, nil
}

func (m *fakeManager) Status(string) (*flow.RunState, error) {
	select {
	case st := <-m.state:
		m.last = st
	default:
	}
	return m.last, nil
}

func TestFlowRunnerAskThenMerged(t *testing.T) {
	m := &fakeManager{state: make(chan *flow.RunState, 2), last: &flow.RunState{Status: "running"}}
	r := &flowRunner{m: m, poll: time.Millisecond}
	h, err := r.Start(context.Background(), "issue-to-merge", map[string]string{"issue": "7"})
	if err != nil || m.req.Issue != 7 || m.req.Workflow != "issue-to-merge" {
		t.Fatalf("err=%v req=%+v", err, m.req)
	}
	m.state <- &flow.RunState{Status: "paused", UpdatedAt: time.Now()}
	select {
	case <-h.Asks():
	case <-time.After(wait):
		t.Fatal("no ask")
	}
	m.state <- &flow.RunState{Status: "done", PR: 9}
	select {
	case res := <-h.Done():
		if res.Outcome != "merged" {
			t.Fatalf("%+v", res)
		}
	case <-time.After(wait):
		t.Fatal("no result")
	}
}

func TestFlowRunnerRejectsOtherInputs(t *testing.T) {
	r := NewRunner(&fakeManager{})
	if _, err := r.Start(context.Background(), "roadmap", map[string]string{"input": "{}"}); err == nil {
		t.Fatal("want error")
	}
}
