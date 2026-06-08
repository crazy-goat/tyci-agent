package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type TodoTool struct{}

type todoItem struct {
	ID       int
	Content  string
	Status   string
	Priority string
	ParentID int
}

var todoState = struct {
	sync.Mutex
	nextID int
	items  []todoItem
}{nextID: 1}

func (t *TodoTool) Name() string { return "todo" }

func (t *TodoTool) Run(ctx context.Context, input map[string]any) ToolResult {
	action := stringParam(input, "action", "list")
	id := intParam(input, "id", 0)
	content := stringParam(input, "content", "")
	status := stringParam(input, "status", "")
	priority := stringParam(input, "priority", "")
	parentID := intParam(input, "parentId", 0)

	todoState.Lock()
	defer todoState.Unlock()

	switch action {
	case "add":
		if content == "" {
			return ToolResult{Type: "result", Success: false, Error: "content required"}
		}
		if status == "" {
			status = "todo"
		}
		if priority == "" {
			priority = "normal"
		}
		if !validStatus(status) || !validPriority(priority) {
			return ToolResult{Type: "result", Success: false, Error: "invalid status or priority"}
		}
		item := todoItem{ID: todoState.nextID, Content: content, Status: status, Priority: priority, ParentID: parentID}
		todoState.nextID++
		todoState.items = append(todoState.items, item)
	case "update":
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo not found"}
		}
		if content != "" {
			todoState.items[idx].Content = content
		}
		if status != "" {
			if !validStatus(status) {
				return ToolResult{Type: "result", Success: false, Error: "invalid status"}
			}
			todoState.items[idx].Status = status
		}
		if priority != "" {
			if !validPriority(priority) {
				return ToolResult{Type: "result", Success: false, Error: "invalid priority"}
			}
			todoState.items[idx].Priority = priority
		}
		if _, ok := input["parentId"]; ok {
			todoState.items[idx].ParentID = parentID
		}
	case "done":
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo not found"}
		}
		todoState.items[idx].Status = "done"
	case "remove":
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo not found"}
		}
		todoState.items = append(todoState.items[:idx], todoState.items[idx+1:]...)
	case "clear":
		todoState.items = nil
		todoState.nextID = 1
	case "list":
		// no-op
	default:
		return ToolResult{Type: "result", Success: false, Error: "invalid action"}
	}

	return ToolResult{Type: "result", Success: true, Content: formatTodosLocked()}
}

func findTodoIndex(id int) int {
	for i, item := range todoState.items {
		if item.ID == id {
			return i
		}
	}
	return -1
}

func formatTodosLocked() string {
	if len(todoState.items) == 0 {
		return "Todo list is empty."
	}
	items := append([]todoItem(nil), todoState.items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		if item.ParentID > 0 {
			fmt.Fprintf(&b, "%d. [%s] %s parent:%d %s", item.ID, item.Status, item.Priority, item.ParentID, item.Content)
		} else {
			fmt.Fprintf(&b, "%d. [%s] %s %s", item.ID, item.Status, item.Priority, item.Content)
		}
	}
	return b.String()
}

func validStatus(s string) bool {
	switch s {
	case "todo", "doing", "done", "blocked":
		return true
	}
	return false
}

func validPriority(s string) bool {
	switch s {
	case "low", "normal", "high":
		return true
	}
	return false
}
