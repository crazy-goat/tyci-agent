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

// MaxParallel limits the dispatcher to concurrent calls of this tool from a
// single LLM response. Return 1 to force sequential execution when the model
// batches several todo calls into one tool-call block — todo mutates shared
// in-process state and concurrent calls race even though Run holds the
// per-call mutex. Other tools omit this method (treated as 0 = unbounded).
func (t *TodoTool) MaxParallel() int { return 1 }

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
			return ToolResult{Type: "result", Success: false, Error: "missing required field \"content\" — fix: todo(action=\"add\", content=\"Write integration tests\") [defaults: status=todo, priority=normal]"}
		}
		st, prio, perr := normalizeAddFields("", status, priority)
		if perr != "" {
			return ToolResult{Type: "result", Success: false, Error: perr}
		}
		item := todoItem{ID: todoState.nextID, Content: content, Status: st, Priority: prio, ParentID: parentID}
		if _, hasParent := input["parentId"]; hasParent && parentID != 0 && findTodoIndex(parentID) < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(add content=%q) — parent doesn't exist — use 0 to add a top-level todo, or pick from existing ids [%s]", parentID, content, existingIDsLocked())}
		}
		todoState.nextID++
		todoState.items = append(todoState.items, item)
	case "add_batch":
		rawItems, ok := input["items"]
		if !ok || rawItems == nil {
			return ToolResult{Type: "result", Success: false, Error: "todo(add_batch) requires \"items\" — fix: todo(action=\"add_batch\", items=[{content:\"...\"}, {content:\"...\"}]) — each entry takes content (required), status, priority, parentId (optional)"}
		}
		items, ok := rawItems.([]any)
		if !ok {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch) requires \"items\" to be an array — got %T — fix: pass items=[{content:\"...\"}, ...]", rawItems)}
		}
		if len(items) == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(add_batch) requires at least one entry — fix: items=[{content:\"first\"}, {content:\"second\"}] — for a single item use todo(action=\"add\", content=\"...\")"}
		}
		// Pre-validate every entry before mutating state, so a bad item in the
		// middle doesn't leave us with a partially-applied batch.
		type prepared struct {
			content   string
			status    string
			priority  string
			parentID  int
			hasParent bool
		}
		prep := make([]prepared, 0, len(items))
		for i, raw := range items {
			m, ok := raw.(map[string]any)
			if !ok {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] must be an object — got %T — fix: items=[{content:\"...\"}, ...]", i, raw)}
			}
			c := stringParam(m, "content", "")
			if c == "" {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] missing required \"content\" — fix: every entry needs content; drop the entry or set content=\"...\"", i)}
			}
			s := stringParam(m, "status", "")
			p := stringParam(m, "priority", "")
			st, prio, perr := normalizeAddFields("", s, p)
			if perr != "" {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] (content=%q) — %s", i, c, perr)}
			}
			pid := intParam(m, "parentId", 0)
			_, hasParent := m["parentId"]
			if hasParent && pid != 0 && findTodoIndex(pid) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] (content=%q) — invalid parentId=%d, doesn't exist — fix: use 0 for top-level, or pick from existing ids [%s]", i, c, pid, existingIDsLocked())}
			}
			prep = append(prep, prepared{content: c, status: st, priority: prio, parentID: pid, hasParent: hasParent})
		}
		// All entries valid — append atomically under the held lock.
		for _, p := range prep {
			todoState.items = append(todoState.items, todoItem{
				ID:       todoState.nextID,
				Content:  p.content,
				Status:   p.status,
				Priority: p.priority,
				ParentID: p.parentID,
			})
			todoState.nextID++
		}
	case "update":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(update) requires an \"id\" — fix: todo(action=\"list\") to read ids, then todo(action=\"update\", id=N, ...) with at least one of content/status/priority/parentId"}
		}
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(update) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry with one of those ids", id, existingIDsLocked())}
		}
		changed := false
		if content != "" {
			todoState.items[idx].Content = content
			changed = true
		}
		if status != "" {
			if !validStatus(status) {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid status=%q for todo(update id=%d) — allowed: todo, doing, done, blocked — current status=%q — fix: drop \"status\" to keep it, or pick one of the allowed values", status, id, todoState.items[idx].Status)}
			}
			todoState.items[idx].Status = status
			changed = true
		}
		if priority != "" {
			if !validPriority(priority) {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid priority=%q for todo(update id=%d) — allowed: low, normal, high — current priority=%q — fix: drop \"priority\" to keep it, or pick one of the allowed values", priority, id, todoState.items[idx].Priority)}
			}
			todoState.items[idx].Priority = priority
			changed = true
		}
		if _, ok := input["parentId"]; ok {
			if parentID != 0 && findTodoIndex(parentID) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(update id=%d) — parent doesn't exist — use 0 to detach, or pick from existing ids [%s]", parentID, id, existingIDsLocked())}
			}
			todoState.items[idx].ParentID = parentID
			changed = true
		}
		if !changed {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(update id=%d) specified no field to change — fix: pass at least one of content=\"...\", status=todo|doing|done|blocked, priority=low|normal|high, parentId=<int>", id)}
		}
	case "doing", "blocked":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(%s) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry", action)}
		}
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(%s) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry", id, action, existingIDsLocked())}
		}
		todoState.items[idx].Status = action
	case "done":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(done) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry"}
		}
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(done) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry", id, existingIDsLocked())}
		}
		todoState.items[idx].Status = "done"
	case "remove":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(remove) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry; use todo(action=\"clear\") to wipe the whole list"}
		}
		idx := findTodoIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(remove) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry, or todo(action=\"clear\") to wipe all", id, existingIDsLocked())}
		}
		todoState.items = append(todoState.items[:idx], todoState.items[idx+1:]...)
	case "clear":
		todoState.items = nil
		todoState.nextID = 1
	case "list":
		// no-op
	default:
		if action == "" {
			return ToolResult{Type: "result", Success: false, Error: "invalid action=\"\" — allowed actions: add, update, doing, done, blocked, remove, list, clear — fix: pass action=\"...\" with one of the values above; default when omitted is \"list\""}
		}
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid action=%q — allowed actions: add, update, doing, done, blocked, remove, list, clear — fix: pick one of the allowed actions above", action)}
	}

	return ToolResult{Type: "result", Success: true, Content: formatTodosLocked()}
}

// existingIDsLocked returns a comma-separated list of current todo ids, or
// "empty" when none exist. Caller must hold todoState.
func existingIDsLocked() string {
	if len(todoState.items) == 0 {
		return "empty"
	}
	ids := make([]string, 0, len(todoState.items))
	for _, it := range todoState.items {
		ids = append(ids, fmt.Sprintf("%d", it.ID))
	}
	return strings.Join(ids, ", ")
}

func findTodoIndex(id int) int {
	for i, item := range todoState.items {
		if item.ID == id {
			return i
		}
	}
	return -1
}

// normalizeAddFields applies defaults (status=todo, priority=normal) and
// validates the result. Returns the normalized pair or a non-empty error
// ready to surface to the LLM. The first arg is unused — kept as a small
// placeholder for future per-action nuance (e.g. add_block starting status).
func normalizeAddFields(_ string, status string, priority string) (string, string, string) {
	if status == "" {
		status = "todo"
	}
	if priority == "" {
		priority = "normal"
	}
	if !validStatus(status) {
		return "", "", fmt.Sprintf("invalid status=%q — allowed: todo, doing, done, blocked — fix: drop \"status\" to use default \"todo\", or pick one of the allowed values", status)
	}
	if !validPriority(priority) {
		return "", "", fmt.Sprintf("invalid priority=%q — allowed: low, normal, high — fix: drop \"priority\" to use default \"normal\", or pick one of the allowed values", priority)
	}
	return status, priority, ""
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
