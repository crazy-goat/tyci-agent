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
	ParentID int
}

type todoItem struct {
	ID       int
	Content  string
	Status   string
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
		out[i] = TodoItem{ID: it.ID, Content: it.Content, Status: it.Status, ParentID: it.ParentID}
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
	// Canonicalised as it arrives, so every path below — update, add,
	// add_batch — sees this list's own vocabulary. See canonicalStatus.
	status := canonicalStatus(stringParam(input, "status", ""))
	parentID := intParam(input, "parentId", 0)

	todoState.Lock()
	defer todoState.Unlock()

	switch action {
	case "add":
		if content == "" {
			return ToolResult{Type: "result", Success: false, Error: "missing required field \"content\" — fix: todo(action=\"add\", content=\"Write integration tests\") [defaults: status=todo]"}
		}
		st, perr := normalizeAddFields("", status)
		if perr != "" {
			return ToolResult{Type: "result", Success: false, Error: perr}
		}
		item := todoItem{ID: todoState.nextID, Content: content, Status: st, ParentID: parentID}
		if _, hasParent := input["parentId"]; hasParent && parentID != 0 && findTodoIndex(parentID) < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(add content=%q) — parent doesn't exist — use 0 to add a top-level todo, or pick from existing ids [%s]", parentID, content, existingIDsLocked())}
		}
		todoState.nextID++
		todoState.items = append(todoState.items, item)
	case "add_batch":
		rawItems, ok := input["items"]
		if !ok || rawItems == nil {
			return ToolResult{Type: "result", Success: false, Error: "todo(add_batch) requires \"items\" — fix: todo(action=\"add_batch\", items=[{content:\"...\"}, {content:\"...\"}]) — each entry takes content (required), status, parentId (optional)"}
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
			st, perr := normalizeAddFields("", s)
			if perr != "" {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] (content=%q) — %s", i, c, perr)}
			}
			pid := intParam(m, "parentId", 0)
			_, hasParent := m["parentId"]
			if hasParent && pid != 0 && findTodoIndex(pid) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] (content=%q) — invalid parentId=%d, doesn't exist — fix: use 0 for top-level, or pick from existing ids [%s]", i, c, pid, existingIDsLocked())}
			}
			prep = append(prep, prepared{content: c, status: st, parentID: pid, hasParent: hasParent})
		}
		// All entries valid — append atomically under the held lock.
		for _, p := range prep {
			todoState.items = append(todoState.items, todoItem{
				ID:       todoState.nextID,
				Content:  p.content,
				Status:   p.status,
				ParentID: p.parentID,
			})
			todoState.nextID++
		}
	case "update":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(update) requires an \"id\" — fix: todo(action=\"list\") to read ids, then todo(action=\"update\", id=N, ...) with at least one of content/status/parentId"}
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
		if _, ok := input["parentId"]; ok {
			if parentID != 0 && findTodoIndex(parentID) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(update id=%d) — parent doesn't exist — use 0 to detach, or pick from existing ids [%s]", parentID, id, existingIDsLocked())}
			}
			todoState.items[idx].ParentID = parentID
			changed = true
		}
		if !changed {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(update id=%d) specified no field to change — fix: pass at least one of content=\"...\", status=todo|doing|done|blocked, parentId=<int>", id)}
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

// normalizeAddFields applies defaults (status=todo) and validates the result.
// Returns the normalized status or a non-empty error ready to surface to the
// LLM. The first arg is unused — kept as a small placeholder for future
// per-action nuance (e.g. add_block starting status).
func normalizeAddFields(_ string, status string) (string, string) {
	if status == "" {
		status = "todo"
	}
	status = canonicalStatus(status)
	if !validStatus(status) {
		return "", fmt.Sprintf("invalid status=%q — allowed: todo, doing, done, blocked — fix: drop \"status\" to use default \"todo\", or pick one of the allowed values", status)
	}
	return status, ""
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
		return fmt.Sprintf("%d. [%s] parent:%d %s", item.ID, item.Status, item.ParentID, item.Content)
	}
	return fmt.Sprintf("%d. [%s] %s", item.ID, item.Status, item.Content)
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

// HasPendingTodos returns true when the todo list contains at least one
// item with status "todo" or "doing". Used by the agent loop to enforce
// the "plan first" policy — non-todo tools are blocked unless the agent
// has active (uncompleted) work in its plan. Once all items are "done"
// or "blocked", the guard re-engages and the LLM must add a new plan
// or reopen an existing item before using other tools.
func HasPendingTodos() bool {
	todoState.Lock()
	defer todoState.Unlock()
	for _, it := range todoState.items {
		if it.Status == "todo" || it.Status == "doing" {
			return true
		}
	}
	return false
}

// TodoCounts returns the number of done items and the total number of items
// in the todo list. Used by the TUI top bar to display a quick summary like
// "todos: 3/10" (3 done out of 10 total).
func TodoCounts() (done int, total int) {
	todoState.Lock()
	defer todoState.Unlock()
	for _, it := range todoState.items {
		total++
		if it.Status == "done" {
			done++
		}
	}
	return
}

func validStatus(s string) bool {
	switch s {
	case "todo", "doing", "done", "blocked":
		return true
	}
	return false
}

// canonicalStatus maps the names models actually reach for onto this list's
// own.
//
// Not leniency for its own sake. Models arrive with "in_progress",
// "pending" and "completed" trained into them by other harnesses, and
// rejecting those buys nothing: the intent is unambiguous every time, and the
// refusal costs a wasted call plus, in one observed session, an identical
// retry. Only unambiguous synonyms are mapped — anything else still fails
// loudly, because a status nobody can guess the meaning of is a real error.
func canonicalStatus(s string) string {
	trimmed := strings.ToLower(strings.TrimSpace(s))
	if validStatus(trimmed) {
		// Already one of ours, possibly with stray spacing or capitals.
		return trimmed
	}
	switch trimmed {
	case "in_progress", "in-progress", "inprogress", "active", "working", "started":
		return "doing"
	case "pending", "open", "not_started", "not-started", "waiting":
		return "todo"
	case "completed", "complete", "finished", "closed":
		return "done"
	case "cancelled", "canceled", "skipped", "stuck":
		return "blocked"
	}
	return s
}
