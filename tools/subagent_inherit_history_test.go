package tools

import (
	"context"
	"testing"

	"github.com/decodo/tyci/connector"
)

// ─── parsing: inherit_history reaches subagentTask ─────────────────────────

func TestParseTasks_InheritHistory_SingleTask(t *testing.T) {
	tasks, err := parseTasks(map[string]any{"task": "x", "inherit_history": true}, "model")
	if err != nil {
		t.Fatalf("parseTasks() error: %v", err)
	}
	if len(tasks) != 1 || !tasks[0].InheritHistory {
		t.Fatalf("expected InheritHistory=true, got %+v", tasks)
	}
}

func TestParseTasks_InheritHistory_DefaultsFalse(t *testing.T) {
	tasks, err := parseTasks(map[string]any{"task": "x"}, "model")
	if err != nil {
		t.Fatalf("parseTasks() error: %v", err)
	}
	if len(tasks) != 1 || tasks[0].InheritHistory {
		t.Fatalf("expected InheritHistory=false by default, got %+v", tasks)
	}
}

func TestParseTasks_InheritHistory_PerItemInTasksArray(t *testing.T) {
	tasks, err := parseTasks(map[string]any{"tasks": []any{
		map[string]any{"task": "a", "inherit_history": true},
		map[string]any{"task": "b"},
	}}, "model")
	if err != nil {
		t.Fatalf("parseTasks() error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if !tasks[0].InheritHistory {
		t.Errorf("expected tasks[0].InheritHistory=true, got false")
	}
	if tasks[1].InheritHistory {
		t.Errorf("expected tasks[1].InheritHistory=false, got true")
	}
}

// ─── runSingleTask: inherit_history seeds opts.History from context ───────

func TestRunSingleTask_InheritHistory_SeedsOptsHistoryFromContext(t *testing.T) {
	history := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier question"}}},
		{Role: "assistant", Content: []connector.ContentBlock{{Type: "text", Text: "earlier answer"}}},
	}
	ctx := connector.WithConversation(context.Background(), history)

	r := &recordingRunner{}
	task := subagentTask{Task: "do the thing", InheritHistory: true}
	runSingleTask(ctx, r, task, 0, true)

	if !r.called {
		t.Fatal("expected the runner to be called")
	}
	if len(r.gotOpts.History) != len(history) {
		t.Fatalf("expected opts.History to carry the %d-message parent conversation, got %d messages: %+v",
			len(history), len(r.gotOpts.History), r.gotOpts.History)
	}
	for i := range history {
		if r.gotOpts.History[i].Content[0].Text != history[i].Content[0].Text {
			t.Errorf("History[%d] = %+v, want %+v", i, r.gotOpts.History[i], history[i])
		}
	}
}

// TestRunSingleTask_NoInheritHistory_OptsHistoryStaysNil is the control: a
// task that does not ask to inherit history must not pick any up even when
// the context carries some (e.g. a nested call inside a round that itself
// inherited history).
func TestRunSingleTask_NoInheritHistory_OptsHistoryStaysNil(t *testing.T) {
	history := []connector.Message{
		{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "earlier question"}}},
	}
	ctx := connector.WithConversation(context.Background(), history)

	r := &recordingRunner{}
	task := subagentTask{Task: "do the thing"}
	runSingleTask(ctx, r, task, 0, true)

	if r.gotOpts.History != nil {
		t.Fatalf("expected opts.History to stay nil without inherit_history, got %+v", r.gotOpts.History)
	}
}

// TestRunSingleTask_InheritHistory_NoConversationInContext covers the case
// documented on subagentTask.InheritHistory: asking to inherit when there is
// nothing to inherit (e.g. a call made outside a running agent.Run round)
// must not fail the call, just leave History empty.
func TestRunSingleTask_InheritHistory_NoConversationInContext(t *testing.T) {
	r := &recordingRunner{}
	task := subagentTask{Task: "do the thing", InheritHistory: true}
	res := runSingleTask(context.Background(), r, task, 0, true)

	if !res.Success {
		t.Fatalf("expected success even with nothing to inherit, got error %q", res.Error)
	}
	if len(r.gotOpts.History) != 0 {
		t.Fatalf("expected empty History, got %+v", r.gotOpts.History)
	}
}
