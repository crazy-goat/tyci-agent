package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector/connectortest"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
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
