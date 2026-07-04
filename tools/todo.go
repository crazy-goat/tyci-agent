package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type TodoTool struct{}

// TodoItem is the exported view of a todo entry. The internal `todoItem`
// keeps the storage struct private; callers outside this package read
// state via AllTodoItems.
type TodoItem struct {
	ID       int
	Content  string
	Status   string
	Priority string
	ParentID int
}

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

// AllTodoItems returns a snapshot of every todo in current state, sorted by id.
// Used by the TUI to enrich todo(doing/done/blocked, N) renders and read
// backwards-compatibly from the display package.
func AllTodoItems() []TodoItem {
	todoState.Lock()
	defer todoState.Unlock()
	out := make([]TodoItem, len(todoState.items))
	for i, it := range todoState.items {
		out[i] = TodoItem{ID: it.ID, Content: it.Content, Status: it.Status, Priority: it.Priority, ParentID: it.ParentID}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

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
	case "doing", "blocked":
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo not found"}
		}
		todoState.items[idx].Status = action
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

// ClearTodoList resets the todo list (used on /new).
func ClearTodoList() {
	todoState.Lock()
	todoState.items = nil
	todoState.nextID = 1
	todoState.Unlock()
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
		b.WriteString(formatTodoLine(item))
	}
	return b.String()
}

func formatTodoLine(item todoItem) string {
	if item.ParentID > 0 {
		return fmt.Sprintf("%d. [%s] %s parent:%d %s", item.ID, item.Status, item.Priority, item.ParentID, item.Content)
	}
	return fmt.Sprintf("%d. [%s] %s %s", item.ID, item.Status, item.Priority, item.Content)
}

// PendingTodos returns formatted lines for todo items that are still open,
// i.e. status "todo" or "doing". Items that are "done" or "blocked" are
// excluded ("blocked" is treated as a deliberate, resolved state). The agent
// loop uses this to remind itself before finishing a turn that left work open.
func PendingTodos() []string {
	todoState.Lock()
	defer todoState.Unlock()
	items := append([]todoItem(nil), todoState.items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	var out []string
	for _, item := range items {
		if item.Status != "todo" && item.Status != "doing" {
			continue
		}
		out = append(out, formatTodoLine(item))
	}
	return out
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
