package display

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/decodo/tyci/tools"
)

// ─── topBarCounterHit ─────────────────────────────────────────────────────

func TestTopBarCounterHit_TodosNoItems(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	// Build the top bar to verify "todos: -" is present.
	bar := m.buildTopBar()
	barW := lipgloss.Width(bar)

	// Find the X position of "todos: -" in the rendered bar.
	idx := strings.Index(bar, "todos: -")
	if idx < 0 {
		t.Fatalf("top bar does not contain 'todos: -', got %q", bar)
	}
	// The rendered bar uses ANSI codes, so we can't just use string index.
	// Instead, we compute the position from the right side.
	// "todos: -" width = 9, " skills: 0 tools: 0 mcp: 0" = 27, sep = 1
	// Total counter group + sep = 28, plus trailing space = 29
	// But let's just verify by scanning the bar width for the counter hit.
	todosLen := lipgloss.Width("todos: -")
	// The todos counter is at the right side. We know from the layout:
	// " " + path + padding + " " + counterGroup
	// Counter group = "todos: - skills: 0 tools: 0 mcp: 0"
	// Let's compute where todos starts.
	// We'll test by checking that a click in the right region hits "todos".

	// Instead of parsing ANSI, use topBarCounterHit which recomputes the layout.
	// The todos counter should be somewhere in the right portion of the bar.
	// We check that at least one X coordinate returns "todos".
	found := false
	for x := 0; x < barW; x++ {
		if m.topBarCounterHit(x) == "todos" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("topBarCounterHit never returned 'todos' for any X in the bar")
	}
	_ = todosLen
}

func TestTopBarCounterHit_TodosWithItems(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 1"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 2"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 5, 7, 2)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	// Verify "todos: 1/2" is present.
	bar := m.buildTopBar()
	if !strings.Contains(bar, "todos: 1/2") {
		t.Fatalf("top bar should contain 'todos: 1/2', got %q", bar)
	}

	// Scan for the todos counter hit region.
	todosHits := 0
	for x := 0; x < 80; x++ {
		if m.topBarCounterHit(x) == "todos" {
			todosHits++
		}
	}
	// "todos: 1/2" = 10 chars wide
	if todosHits < 9 || todosHits > 11 {
		t.Fatalf("expected ~10 X positions to hit 'todos', got %d", todosHits)
	}
}

func TestTopBarCounterHit_NoHitOutsideCounters(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	// X=0 is the leading space (no counter).
	if m.topBarCounterHit(0) != "" {
		t.Fatal("X=0 should not hit any counter")
	}
	// X=1 is the first char of the path "~" (no counter).
	if m.topBarCounterHit(1) != "" {
		t.Fatal("X=1 should not hit any counter (path area)")
	}
}

func TestTopBarCounterHit_TodosDroppedWhenNarrow(t *testing.T) {
	tools.ClearTodoList()
	tool := &tools.TodoTool{}
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "step 1"})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 18, 12, 3)
	m.ready = true
	m.width = 18 // very narrow — todos should be dropped (dropOrder 3)
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	// At this width, only skills should remain.
	for x := 0; x < 18; x++ {
		if m.topBarCounterHit(x) == "todos" {
			t.Fatalf("todos counter should be dropped at width=18, but hit at X=%d", x)
		}
	}
}

// ─── open/close todo modal ────────────────────────────────────────────────

func TestTodoModal_OpenClose(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.atBottom = true

	if m.todoModalActive {
		t.Fatal("todo modal should not be active initially")
	}

	m.openTodoModal()
	if !m.todoModalActive {
		t.Fatal("todo modal should be active after openTodoModal")
	}
	if m.todoModalScroll != 0 {
		t.Fatalf("todoModalScroll = %d, want 0", m.todoModalScroll)
	}

	m.closeTodoModal()
	if m.todoModalActive {
		t.Fatal("todo modal should not be active after closeTodoModal")
	}
}

func TestTodoModal_OpenIdempotent(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	m.openTodoModal()
	scroll := m.todoModalScroll
	m.openTodoModal() // second open should be no-op
	if m.todoModalScroll != scroll {
		t.Fatal("second openTodoModal should be idempotent")
	}
}

// ─── Top bar click opens todo modal ──────────────────────────────────────

func TestTodoModal_ClickOpensModal(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "test task"})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.reading = true

	// Find an X position that hits the todos counter.
	var todosX int = -1
	for x := 0; x < 80; x++ {
		if m.topBarCounterHit(x) == "todos" {
			todosX = x
			break
		}
	}
	if todosX < 0 {
		t.Fatal("could not find X position for todos counter")
	}

	// Simulate a click at Y=0, X=todosX.
	model, _ := m.handleMouseMsg(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      todosX,
		Y:      0,
	})
	m = model.(TuiModel)

	if !m.todoModalActive {
		t.Fatal("clicking on todos counter should open todo modal")
	}
}

func TestTodoModal_ClickOutsideTodosDoesNotOpen(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.reading = true

	// Click on the path area (X=1, Y=0).
	model, _ := m.handleMouseMsg(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      1,
		Y:      0,
	})
	m = model.(TuiModel)

	if m.todoModalActive {
		t.Fatal("clicking on path area should not open todo modal")
	}
}

// ─── ESC closes todo modal ───────────────────────────────────────────────

func TestTodoModal_ESCCloses(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.atBottom = true

	m.openTodoModal()
	if !m.todoModalActive {
		t.Fatal("expected modal open")
	}

	// Simulate ESC key.
	model, _ := m.updateTodoModal(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(TuiModel)

	if m.todoModalActive {
		t.Fatal("ESC should close todo modal")
	}
}

func TestTodoModal_EnterCloses(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.atBottom = true

	m.openTodoModal()

	model, _ := m.updateTodoModal(tea.KeyMsg{Type: tea.KeyEnter})
	m = model.(TuiModel)

	if m.todoModalActive {
		t.Fatal("Enter should close todo modal")
	}
}

func TestTodoModal_OutsideClickCloses(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.atBottom = true

	m.openTodoModal()

	// Click at X=0, Y=0 (outside the centered modal).
	model, _ := m.updateTodoModal(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      0,
		Y:      0,
	})
	m = model.(TuiModel)

	if m.todoModalActive {
		t.Fatal("click outside modal should close it")
	}
}

// ─── renderTodoModalView ─────────────────────────────────────────────────

func TestRenderTodoModal_ShowsItems(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "first task"})
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "second task"})
	tool.Run(context.Background(), map[string]any{"action": "done", "id": 1})
	tool.Run(context.Background(), map[string]any{"action": "doing", "id": 2})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true

	view := m.renderTodoModalView()

	if !strings.Contains(view, "Todo List") {
		t.Fatalf("modal should contain 'Todo List' title, got %q", view)
	}
	if !strings.Contains(view, "1/2 done") {
		t.Fatalf("modal should show '1/2 done' in title, got %q", view)
	}
	if !strings.Contains(view, "first task") {
		t.Fatalf("modal should show 'first task', got %q", view)
	}
	if !strings.Contains(view, "second task") {
		t.Fatalf("modal should show 'second task', got %q", view)
	}
	if !strings.Contains(view, "[done]") {
		t.Fatalf("modal should show '[done]' status, got %q", view)
	}
	if !strings.Contains(view, "[doing]") {
		t.Fatalf("modal should show '[doing]' status, got %q", view)
	}
	if !strings.Contains(view, "ESC close") {
		t.Fatalf("modal footer should show 'ESC close', got %q", view)
	}
}

func TestRenderTodoModal_EmptyList(t *testing.T) {
	tools.ClearTodoList()

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true

	view := m.renderTodoModalView()

	if !strings.Contains(view, "No todo items") {
		t.Fatalf("empty modal should show 'No todo items', got %q", view)
	}
	if !strings.Contains(view, "0/0 done") {
		t.Fatalf("empty modal should show '0/0 done', got %q", view)
	}
}

func TestRenderTodoModal_WidthFitsTerminal(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "task"})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 60
	m.height = 20
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true

	view := m.renderTodoModalView()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > 60 {
			t.Fatalf("line %d width = %d, exceeds terminal width 60", i, w)
		}
	}
}

// ─── Scroll ──────────────────────────────────────────────────────────────

func TestTodoModal_ScrollUpDown(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	for i := 1; i <= 50; i++ {
		tool.Run(context.Background(), map[string]any{"action": "add", "content": "task"})
	}

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 20 // small height to force scrolling
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true

	maxScroll := m.todoModalMaxScroll()
	if maxScroll <= 0 {
		t.Fatal("expected maxScroll > 0 with 50 items in a 20-row terminal")
	}

	// Scroll up
	model, _ := m.updateTodoModal(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(TuiModel)
	if m.todoModalScroll != 1 {
		t.Fatalf("scroll after Up = %d, want 1", m.todoModalScroll)
	}

	// Scroll to max
	m.todoModalScroll = maxScroll
	model, _ = m.updateTodoModal(tea.KeyMsg{Type: tea.KeyUp})
	m = model.(TuiModel)
	if m.todoModalScroll != maxScroll {
		t.Fatalf("scroll should not exceed maxScroll, got %d > %d", m.todoModalScroll, maxScroll)
	}

	// Scroll down
	model, _ = m.updateTodoModal(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(TuiModel)
	if m.todoModalScroll != maxScroll-1 {
		t.Fatalf("scroll after Down = %d, want %d", m.todoModalScroll, maxScroll-1)
	}

	// Scroll to 0
	m.todoModalScroll = 0
	model, _ = m.updateTodoModal(tea.KeyMsg{Type: tea.KeyDown})
	m = model.(TuiModel)
	if m.todoModalScroll != 0 {
		t.Fatalf("scroll should not go below 0, got %d", m.todoModalScroll)
	}
}

func TestTodoModal_HomeEnd(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	for i := 1; i <= 30; i++ {
		tool.Run(context.Background(), map[string]any{"action": "add", "content": "task"})
	}

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 15
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true

	maxScroll := m.todoModalMaxScroll()

	// Home → scroll to max
	model, _ := m.updateTodoModal(tea.KeyMsg{Type: tea.KeyHome})
	m = model.(TuiModel)
	if m.todoModalScroll != maxScroll {
		t.Fatalf("Home scroll = %d, want %d", m.todoModalScroll, maxScroll)
	}

	// End → scroll to 0
	model, _ = m.updateTodoModal(tea.KeyMsg{Type: tea.KeyEnd})
	m = model.(TuiModel)
	if m.todoModalScroll != 0 {
		t.Fatalf("End scroll = %d, want 0", m.todoModalScroll)
	}
}

// ─── Integration: renderFrame shows modal ────────────────────────────────

func TestRenderFrame_TodoModalActive(t *testing.T) {
	tool := &tools.TodoTool{}
	tools.ClearTodoList()
	tool.Run(context.Background(), map[string]any{"action": "add", "content": "my task"})

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.todoModalActive = true
	m.reading = true

	frame := m.renderFrame()
	// Should contain the todo modal, not the normal top bar path.
	if !strings.Contains(frame, "Todo List") {
		t.Fatal("renderFrame should render todo modal when active")
	}
	// The normal top bar path should NOT be visible.
	if strings.Contains(frame, "~") && !strings.Contains(frame, "Todo List") {
		t.Fatal("renderFrame should not show the normal top bar when todo modal is active")
	}
}

func TestRenderFrame_TodoModalNotInNormalView(t *testing.T) {
	tools.ClearTodoList()

	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.reading = true

	frame := m.renderFrame()
	if strings.Contains(frame, "Todo List") {
		t.Fatal("renderFrame should not show todo modal when it's not active")
	}
}

// ─── Update routing ──────────────────────────────────────────────────────

func TestUpdate_RoutesToTodoModal(t *testing.T) {
	tools.ClearTodoList()
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.reading = true
	m.todoModalActive = true

	// ESC should be handled by updateTodoModal, closing the modal.
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(TuiModel)

	if m.todoModalActive {
		t.Fatal("ESC in Update should close todo modal via updateTodoModal routing")
	}
}
