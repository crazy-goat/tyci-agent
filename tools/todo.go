package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
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

// mainAgentTodoID identifies the top-level conversation's own todo list —
// the one the TUI (top bar, todo modal, todo-tool rendering) always shows,
// regardless of which subagent or /btw side-conversation last wrote to
// *its own* list. It is the empty string precisely because that value is
// never handed out by nextTodoAgentID (see subagent.go) and never a real
// job id (see JobIDCtxKey), so no child can ever collide into it.
//
// AllTodoItems, TodoCounts, PendingTodos, HasPendingTodos and
// ClearTodoList all read/write this id explicitly — a named constant
// rather than a magic "empty context" — because callers without a ctx
// (the display package) or that specifically mean the main conversation
// (the /new handlers, the top-level plan guard wired in commands.go) must
// keep seeing the main list no matter what any child is doing.
const mainAgentTodoID = ""

// todoAgentList is one agent's own todo state: its own id sequence and its
// own items, so a child can never collide ids with its parent or any
// sibling.
type todoAgentList struct {
	nextID       int
	items        []todoItem
	lastActivity time.Time
	// terminal is true once the agent that owns this list is known to have
	// finished (see MarkTodoAgentDone). Only terminal lists are eligible
	// for eviction — a still-running agent's list must never be dropped
	// out from under it, mirroring jobs.Registry's own refusal to prune a
	// running/waiting-answer job (see its maxRetainedTerminalJobs doc
	// comment). A list that is never marked terminal (nothing calls
	// MarkTodoAgentDone for it) is simply never evicted; today every real
	// caller does call it (subagent.go's runSingleTask, btw.go's job
	// bodies).
	terminal bool
}

// maxRetainedChildTodoLists bounds how many non-main agents' TERMINAL todo
// lists this process keeps. A finished subagent's or /btw's list is
// deliberately NOT discarded the moment it finishes — item 1's Subagents
// tab wants to show what a child planned — but without a bound the map
// would grow for the life of the process, the same hazard documented at
// jobs/registry.go's maxRetainedTerminalJobs. 50 mirrors that constant:
// well past the number of finished child lists anyone still cares about,
// evicted oldest-lastActivity-first among terminal lists only.
const maxRetainedChildTodoLists = 50

var todoStore = struct {
	sync.Mutex
	agents map[string]*todoAgentList
}{agents: map[string]*todoAgentList{}}

// getOrCreateLocked returns the todo list for agentID, creating it (with a
// fresh id sequence starting at 1, and lastActivity set immediately — never
// left at the zero time) on first use. Caller must hold todoStore.
//
// Eviction runs BEFORE the new entry is inserted (not after), and only ever
// considers ids other than agentID: a brand-new list must be structurally
// impossible for evictOldChildListsLocked to pick as "oldest", not merely
// unlikely to. It used to run after insertion with lastActivity still at
// its zero value, which made the just-created entry look like the oldest
// thing in the map — evicted immediately, every time, once 50 children
// existed. The caller kept writing into the detached pointer, so this was
// silent: no error, just a list that reset to empty on the next read.
func getOrCreateLocked(agentID string) *todoAgentList {
	if l, ok := todoStore.agents[agentID]; ok {
		l.lastActivity = time.Now()
		return l
	}
	if agentID != mainAgentTodoID {
		evictOldChildListsLocked(agentID)
	}
	l := &todoAgentList{nextID: 1, lastActivity: time.Now()}
	todoStore.agents[agentID] = l
	return l
}

// MarkTodoAgentDone marks agentID's todo list as terminal — eligible for
// eviction once maxRetainedChildTodoLists is exceeded (see
// evictOldChildListsLocked). Call exactly once, when the agent that owns
// this list has actually finished: subagent.go's runSingleTask does this
// via defer for every subagent call (sync or async), and btw.go's job
// bodies do the same for /btw and "resume". A no-op for the main id and for
// an id with no list yet.
func MarkTodoAgentDone(agentID string) {
	if agentID == "" || agentID == mainAgentTodoID {
		return
	}
	todoStore.Lock()
	defer todoStore.Unlock()
	if l, ok := todoStore.agents[agentID]; ok {
		l.terminal = true
	}
}

// evictOldChildListsLocked drops the least-recently-active TERMINAL child
// (non-main, non-excludeID) todo lists beyond maxRetainedChildTodoLists.
// excludeID is always the id about to be inserted by the caller — see
// getOrCreateLocked's doc comment for why it must be excluded on top of
// running before the insert. Caller must hold todoStore.
func evictOldChildListsLocked(excludeID string) {
	type entry struct {
		id string
		t  time.Time
	}
	var terminal []entry
	for id, l := range todoStore.agents {
		if id == mainAgentTodoID || id == excludeID || !l.terminal {
			continue
		}
		terminal = append(terminal, entry{id, l.lastActivity})
	}
	if len(terminal) <= maxRetainedChildTodoLists {
		return
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].t.Before(terminal[j].t) })
	for _, e := range terminal[:len(terminal)-maxRetainedChildTodoLists] {
		delete(todoStore.agents, e.id)
	}
}

// todoAgentIDFromCtx resolves which agent's todo list a tool call should
// read/write. TodoAgentCtxKey (set by subagent.go's runSingleTask for every
// subagent call, sync or async) takes priority; JobIDCtxKey (set for a
// /btw side-conversation — see btw.go's startBtw) is the fallback, so a
// /btw turn gets its own list too, keyed by its job id, without needing its
// own ctx key. Neither present means this is the main conversation itself.
func todoAgentIDFromCtx(ctx context.Context) string {
	if id, ok := ctx.Value(TodoAgentCtxKey{}).(string); ok && id != "" {
		return id
	}
	if id, ok := ctx.Value(JobIDCtxKey{}).(string); ok && id != "" {
		return id
	}
	return mainAgentTodoID
}

// AllTodoItems returns a snapshot of every todo in the MAIN conversation's
// list, sorted by id. Used by the TUI to enrich todo(doing/done/blocked, N)
// renders and read backwards-compatibly from the display package, which has
// no ctx and must always show the main agent's list regardless of which
// subagent or /btw side-conversation last wrote somewhere.
func AllTodoItems() []TodoItem {
	todoStore.Lock()
	defer todoStore.Unlock()
	l := getOrCreateLocked(mainAgentTodoID)
	out := make([]TodoItem, len(l.items))
	for i, it := range l.items {
		out[i] = TodoItem{ID: it.ID, Content: it.Content, Status: it.Status, ParentID: it.ParentID}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t *TodoTool) Name() string { return "todo" }

// MaxParallel limits the dispatcher to concurrent calls of this tool from a
// single LLM response. Return 1 to force sequential execution when the model
// batches several todo calls into one tool-call block.
//
// Not a data-race concern any more — every agent's list lives behind one
// mutex (todoStore) that each Run call holds for its whole body, so
// concurrent calls cannot corrupt state. It is an ORDERING concern: a
// single LLM response that batches todo(action="add") followed by
// todo(action="update", id=N) for the id the add just produced must run in
// that order, not whichever way the dispatcher happens to schedule two
// goroutines. Keep this at 1 even if the locking story changes.
func (t *TodoTool) MaxParallel() int { return 1 }

func (t *TodoTool) Run(ctx context.Context, input map[string]any) ToolResult {
	action := stringParam(input, "action", "list")
	id := intParam(input, "id", 0)
	content := stringParam(input, "content", "")
	// Canonicalised as it arrives, so every path below — update, add,
	// add_batch — sees this list's own vocabulary. See canonicalStatus.
	status := canonicalStatus(stringParam(input, "status", ""))
	parentID := intParam(input, "parentId", 0)

	todoStore.Lock()
	defer todoStore.Unlock()
	l := getOrCreateLocked(todoAgentIDFromCtx(ctx))

	switch action {
	case "add":
		if content == "" {
			return ToolResult{Type: "result", Success: false, Error: "missing required field \"content\" — fix: todo(action=\"add\", content=\"Write integration tests\") [defaults: status=todo]"}
		}
		st, perr := normalizeAddFields("", status)
		if perr != "" {
			return ToolResult{Type: "result", Success: false, Error: perr}
		}
		item := todoItem{ID: l.nextID, Content: content, Status: st, ParentID: parentID}
		if _, hasParent := input["parentId"]; hasParent && parentID != 0 && l.findIndex(parentID) < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(add content=%q) — parent doesn't exist — use 0 to add a top-level todo, or pick from existing ids [%s]", parentID, content, l.existingIDs())}
		}
		l.nextID++
		l.items = append(l.items, item)
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
			if hasParent && pid != 0 && l.findIndex(pid) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(add_batch): items[%d] (content=%q) — invalid parentId=%d, doesn't exist — fix: use 0 for top-level, or pick from existing ids [%s]", i, c, pid, l.existingIDs())}
			}
			prep = append(prep, prepared{content: c, status: st, parentID: pid, hasParent: hasParent})
		}
		// All entries valid — append atomically under the held lock.
		for _, p := range prep {
			l.items = append(l.items, todoItem{
				ID:       l.nextID,
				Content:  p.content,
				Status:   p.status,
				ParentID: p.parentID,
			})
			l.nextID++
		}
	case "update":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(update) requires an \"id\" — fix: todo(action=\"list\") to read ids, then todo(action=\"update\", id=N, ...) with at least one of content/status/parentId"}
		}
		idx := l.findIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(update) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry with one of those ids", id, l.existingIDs())}
		}
		changed := false
		if content != "" {
			l.items[idx].Content = content
			changed = true
		}
		if status != "" {
			if !validStatus(status) {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid status=%q for todo(update id=%d) — allowed: todo, doing, done, blocked — current status=%q — fix: drop \"status\" to keep it, or pick one of the allowed values", status, id, l.items[idx].Status)}
			}
			l.items[idx].Status = status
			changed = true
		}
		if _, ok := input["parentId"]; ok {
			if parentID != 0 && l.findIndex(parentID) < 0 {
				return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid parentId=%d for todo(update id=%d) — parent doesn't exist — use 0 to detach, or pick from existing ids [%s]", parentID, id, l.existingIDs())}
			}
			l.items[idx].ParentID = parentID
			changed = true
		}
		if !changed {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(update id=%d) specified no field to change — fix: pass at least one of content=\"...\", status=todo|doing|done|blocked, parentId=<int>", id)}
		}
	case "doing", "blocked":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("todo(%s) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry", action)}
		}
		idx := l.findIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(%s) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry", id, action, l.existingIDs())}
		}
		l.items[idx].Status = action
	case "done":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(done) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry"}
		}
		idx := l.findIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(done) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry", id, l.existingIDs())}
		}
		l.items[idx].Status = "done"
	case "remove":
		if id == 0 {
			return ToolResult{Type: "result", Success: false, Error: "todo(remove) requires an \"id\" — fix: call todo(action=\"list\") to read current ids, then retry; use todo(action=\"clear\") to wipe the whole list"}
		}
		idx := l.findIndex(id)
		if idx < 0 {
			return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid id=%d for todo(remove) — not found — fix: current ids are [%s]; call todo(action=\"list\") then retry, or todo(action=\"clear\") to wipe all", id, l.existingIDs())}
		}
		l.items = append(l.items[:idx], l.items[idx+1:]...)
	case "clear":
		l.items = nil
		l.nextID = 1
	case "list":
		// no-op
	default:
		if action == "" {
			return ToolResult{Type: "result", Success: false, Error: "invalid action=\"\" — allowed actions: add, update, doing, done, blocked, remove, list, clear — fix: pass action=\"...\" with one of the values above; default when omitted is \"list\""}
		}
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("invalid action=%q — allowed actions: add, update, doing, done, blocked, remove, list, clear — fix: pick one of the allowed actions above", action)}
	}

	return ToolResult{Type: "result", Success: true, Content: l.format()}
}

// existingIDs returns a comma-separated list of this list's current todo
// ids, or "empty" when none exist. Caller must hold todoStore.
func (l *todoAgentList) existingIDs() string {
	if len(l.items) == 0 {
		return "empty"
	}
	ids := make([]string, 0, len(l.items))
	for _, it := range l.items {
		ids = append(ids, fmt.Sprintf("%d", it.ID))
	}
	return strings.Join(ids, ", ")
}

func (l *todoAgentList) findIndex(id int) int {
	for i, item := range l.items {
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

// ClearTodoList resets the MAIN conversation's todo list (used on /new).
// It never touches a child's list — a subagent or /btw job still running
// after /new must not be able to write its own todos back into what the
// user just cleared (see this list's own doc comment).
func ClearTodoList() {
	todoStore.Lock()
	l := getOrCreateLocked(mainAgentTodoID)
	l.items = nil
	l.nextID = 1
	todoStore.Unlock()
}

func (l *todoAgentList) format() string {
	if len(l.items) == 0 {
		return "Todo list is empty."
	}
	items := append([]todoItem(nil), l.items...)
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

// PendingTodos returns formatted lines for the MAIN conversation's todo
// items that are still open, i.e. status "todo" or "doing" ("blocked" is
// treated as a deliberate, resolved state). The agent loop uses this to
// remind itself before finishing a turn that left work open — always about
// its own (main) plan, never a subagent's or /btw's.
func PendingTodos() []string {
	todoStore.Lock()
	defer todoStore.Unlock()
	l := getOrCreateLocked(mainAgentTodoID)
	items := append([]todoItem(nil), l.items...)
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

// HasPendingTodos returns true when the MAIN conversation's todo list
// contains at least one item with status "todo" or "doing". Used by the
// agent loop to enforce the "plan first" policy — non-todo tools are
// blocked unless the agent has active (uncompleted) work in its plan.
// Once all items are "done" or "blocked", the guard re-engages and the LLM
// must add a new plan or reopen an existing item before using other tools.
//
// Scoped to the main list on purpose: a subagent's cfg does not wire this
// guard at all today (see main.go's agentRunner.run), so this only ever
// gates the top-level conversation — and even if a child's cfg wired an
// analogous check in the future, it must resolve its own agent's list via
// todoAgentIDFromCtx(ctx), not this one.
func HasPendingTodos() bool {
	todoStore.Lock()
	defer todoStore.Unlock()
	l := getOrCreateLocked(mainAgentTodoID)
	for _, it := range l.items {
		if it.Status == "todo" || it.Status == "doing" {
			return true
		}
	}
	return false
}

// TodoCounts returns the number of done items and the total number of items
// in the MAIN conversation's todo list. Used by the TUI top bar to display
// a quick summary like "todos: 3/10" (3 done out of 10 total) — always the
// main agent's counts, never whichever subagent or /btw job last wrote.
func TodoCounts() (done int, total int) {
	todoStore.Lock()
	defer todoStore.Unlock()
	l := getOrCreateLocked(mainAgentTodoID)
	for _, it := range l.items {
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
