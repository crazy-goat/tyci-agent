package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector/connectortest"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

func TestCompactSessionDropsTrailingUnansweredToolCall(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.Open(filepath.Join(dir, "session.jsonl"), dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msgs := make([]connector.Message, 0, 9)
	for i := 0; i < 8; i++ {
		msgs = append(msgs, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "history"}}})
	}
	msgs = append(msgs, connector.Message{
		Role: "assistant",
		Content: []connector.ContentBlock{
			{Type: "text", Text: "checking"},
			{Type: "toolCall", ID: "call-1", Name: "bash"},
		},
	})
	if _, err := CompactSession(sess, &msgs, "summary", ""); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 9 || len(msgs[len(msgs)-1].Content) != 2 || msgs[len(msgs)-1].Content[1].Type != "toolCall" {
		t.Fatalf("valid unanswered tool call should survive for its result: %#v", msgs)
	}
}

func TestRunCompactAsOnlyToolPreservesToolCallResultPair(t *testing.T) {
	dir := t.TempDir()
	sess, err := session.Open(filepath.Join(dir, "session.jsonl"), dir, "m", "p")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	calls := 0
	var msgs []connector.Message
	compactor := func(summary, focus string) (string, error) { return CompactSession(sess, &msgs, summary, focus) }
	runner := toolRunnerFunc(func(ctx context.Context, name string, args map[string]any) (string, error) {
		calls++
		if name != "compact" {
			t.Fatalf("tool = %s", name)
		}
		res := (&tools.CompactTool{}).Run(tools.WithCompactor(ctx, compactor), args)
		if !res.Success {
			return "", fmt.Errorf("compact: %s", res.Error)
		}
		return res.Content, nil
	})
	mc := &connectortest.Fake{ProviderName: "p", ModelName: "m", Turns: [][]stream.Event{
		{stream.ToolCall{ID: "compact-1", Name: "compact", Arguments: `{"summary":"keep"}`}, stream.Finish{Reason: "tool_calls"}},
		{stream.TextDelta{Text: "done"}, stream.Finish{}},
	}}
	msgs = []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "start"}}}}
	_, err = Run(context.Background(), mc, &silentDisplay{}, &msgs, Config{Tools: runner, Session: sess})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("compact calls = %d", calls)
	}
	if len(msgs) < 4 || msgs[len(msgs)-2].Role != "toolResult" || msgs[len(msgs)-3].Role != "assistant" {
		t.Fatalf("compact tool/result sequence = %#v", msgs)
	}
	if !strings.Contains(msgs[len(msgs)-1].Content[0].Text, "done") {
		t.Fatalf("final assistant answer missing: %#v", msgs)
	}
	data, err := os.ReadFile(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"role":"toolResult"`) && !strings.Contains(string(data), `"toolCallId":"compact-1"`) {
		t.Fatalf("replay lost compact call id: %s", data)
	}
	if msgs[len(msgs)-2].Content[0].ToolCallID != "compact-1" {
		t.Fatalf("result lost call id: %#v", msgs[len(msgs)-2])
	}
}

type toolRunnerFunc func(context.Context, string, map[string]any) (string, error)

func (f toolRunnerFunc) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	return f(ctx, name, args)
}

// #304: when the task is still inside the kept tail, compactInMemory does not
// copy it a second time to the head.
func TestCompactInMemory_TaskInTailNotDuplicated(t *testing.T) {
	text := func(role, s string) connector.Message {
		return connector.Message{Role: role, Content: []connector.ContentBlock{{Type: "text", Text: s}}}
	}
	var msgs []connector.Message
	for i := range 4 {
		msgs = append(msgs, text("user", fmt.Sprintf("old %d", i)), text("assistant", fmt.Sprintf("old reply %d", i)))
	}
	task := text("user", "the task")
	msgs = append(msgs, task)
	for i := range 3 {
		msgs = append(msgs, text("assistant", fmt.Sprintf("work %d", i)), text("user", fmt.Sprintf("result %d", i)))
	}
	if !compactInMemory(&msgs, &task, "note") {
		t.Fatal("compactInMemory returned false")
	}
	count := 0
	for _, m := range msgs {
		if reflect.DeepEqual(m, task) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("task appears %d times, want 1: %+v", count, msgs)
	}
}
