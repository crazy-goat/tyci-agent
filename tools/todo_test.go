package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoTool_StatusAliases(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "x"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "doing", "id": 1})
	if !res.Success || !strings.Contains(res.Content, "[doing]") {
		t.Fatalf("doing failed: %v %s %s", res.Success, res.Content, res.Error)
	}
}

func TestTodoTool_ClearAndList(t *testing.T) {
	tool := &TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "clear"})
	res := tool.Run(context.Background(), map[string]any{"action": "add", "content": "first"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "add", "content": "second"})
	if !res.Success {
		t.Fatalf("add failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !res.Success {
		t.Fatalf("list failed: %s", res.Error)
	}
	if !strings.Contains(res.Content, "first") || !strings.Contains(res.Content, "second") {
		t.Fatalf("expected both items, got: %s", res.Content)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "clear"})
	if !res.Success {
		t.Fatalf("clear failed: %s", res.Error)
	}
	res = tool.Run(context.Background(), map[string]any{"action": "list"})
	if !strings.Contains(res.Content, "Todo list is empty") {
		t.Fatalf("expected empty list, got: %s", res.Content)
	}
}
