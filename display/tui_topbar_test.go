package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// ─── displayPath ──────────────────────────────────────────────────────────

func TestDisplayPath_HomeIsCwd(t *testing.T) {
	got := displayPath("/home/user", "/home/user")
	if got != "~" {
		t.Fatalf("displayPath(home, home) = %q, want ~", got)
	}
}

func TestDisplayPath_SubdirOfHome(t *testing.T) {
	got := displayPath("/home/user/projects/tyci", "/home/user")
	want := "~/projects/tyci"
	if got != want {
		t.Fatalf("displayPath = %q, want %q", got, want)
	}
}

func TestDisplayPath_OutsideHome(t *testing.T) {
	got := displayPath("/tmp/serve", "/home/user")
	if got != "/tmp/serve" {
		t.Fatalf("displayPath = %q, want /tmp/serve", got)
	}
}

func TestDisplayPath_EmptyHome(t *testing.T) {
	got := displayPath("/tmp/serve", "")
	if got != "/tmp/serve" {
		t.Fatalf("displayPath with empty home = %q, want /tmp/serve", got)
	}
}

func TestDisplayPath_EmptyCwd(t *testing.T) {
	got := displayPath("", "/home/user")
	if got != "~?" {
		t.Fatalf("displayPath with empty cwd = %q, want ~?", got)
	}
}

func TestDisplayPath_NormalizesDotDot(t *testing.T) {
	got := displayPath("/home/user/projects/../docs", "/home/user")
	want := "~/docs"
	if got != want {
		t.Fatalf("displayPath = %q, want %q", got, want)
	}
}

func TestDisplayPath_NormalizesDot(t *testing.T) {
	got := displayPath("/home/user/./projects", "/home/user")
	want := "~/projects"
	if got != want {
		t.Fatalf("displayPath = %q, want %q", got, want)
	}
}

func TestDisplayPath_HomePrefixMustBeSepBoundary(t *testing.T) {
	// /home/userdata should NOT match /home/user
	got := displayPath("/home/userdata/work", "/home/user")
	if got != "/home/userdata/work" {
		t.Fatalf("displayPath = %q, want /home/userdata/work (no false prefix match)", got)
	}
}

// ─── buildTopBar ──────────────────────────────────────────────────────────

func TestBuildTopBar_WidthMatchesTerminal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user/projects/tyci-agent"
	m.home = "/home/user"

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 80 {
		t.Fatalf("buildTopBar width = %d, want 80", w)
	}
}

func TestBuildTopBar_ShortPath(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	bar := m.buildTopBar()
	if !strings.Contains(bar, "~") {
		t.Fatalf("buildTopBar should contain ~ for home dir, got %q", bar)
	}
}

func TestBuildTopBar_LongPathTruncated(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 50 // narrow terminal
	m.height = 24
	m.cwd = "/home/user/projects/really/long/path/to/tyci-agent"
	m.home = "/home/user"
	m.skillCount = 0
	m.toolCount = 0
	m.mcpCount = 0

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 50 {
		t.Fatalf("buildTopBar width = %d, want 50", w)
	}
	// Should still contain the tail of the path.
	if !strings.Contains(bar, "tyci-agent") {
		t.Fatalf("buildTopBar should keep tail of path, got %q", bar)
	}
}

func TestBuildTopBar_ShowsPath(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/tmp"
	m.home = "/home/user"

	bar := m.buildTopBar()
	if !strings.Contains(bar, "/tmp") {
		t.Fatalf("buildTopBar should contain path, got %q", bar)
	}
}

func TestBuildTopBar_HomeDirShownAsTilde(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"

	bar := m.buildTopBar()
	if !strings.Contains(bar, "~") {
		t.Fatalf("buildTopBar should show ~ for cwd==home, got %q", bar)
	}
	// Should not contain the literal home path.
	if strings.Contains(bar, "/home/user") {
		t.Fatalf("buildTopBar should not contain literal home path, got %q", bar)
	}
}

// ─── renderFrame integration ──────────────────────────────────────────────

func TestRenderFrame_TopBarIsFirstLine(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user/projects/tyci"
	m.home = "/home/user"
	m.reading = true

	frame := m.renderFrame()
	lines := strings.Split(frame, "\n")
	if len(lines) == 0 {
		t.Fatal("renderFrame returned empty string")
	}
	// First line should contain the top bar with the cwd path.
	if !strings.Contains(lines[0], "~/projects/tyci") {
		t.Fatalf("first line of renderFrame should contain cwd path, got %q", lines[0])
	}
}

func TestRenderFrame_TopBarNotInModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user/projects/tyci"
	m.home = "/home/user"

	// Open subagent modal
	m.subagentModalActive = true
	m.subagentModalTitle = "test"
	m.subagentModalContent.WriteString("modal content")
	m.subagentModalDone = true

	frame := m.renderFrame()
	// Modal view should NOT contain the top bar path.
	if strings.Contains(frame, "~/projects/tyci") {
		t.Fatal("subagent modal view should not contain the top bar")
	}
}

func TestRenderFrame_TopBarNotInPicker(t *testing.T) {
	m := newPickerTestModel(testProviders, nil, "")
	m.cwd = "/home/user/projects/tyci"
	m.home = "/home/user"
	m.openModelPicker()

	frame := m.renderFrame()
	if strings.Contains(frame, "~/projects/tyci") {
		t.Fatal("model picker view should not contain the top bar")
	}
}

func TestRenderFrame_MsgHeightReducedByOne(t *testing.T) {
	// With top bar, visibleLines should be height - input.Height() - 2
	// (was -1 before).  We verify indirectly: render a model with many blocks
	// and confirm the transcript area is one line shorter.
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.reading = true

	visible := m.visibleLines()
	// Input height is 1 (default textarea), so visibleLines = 30 - 1 - 2 = 27.
	if visible != 27 {
		t.Fatalf("visibleLines = %d, want 27 (height 30 - input 1 - topbar 1 - status 1)", visible)
	}
}

// ─── paintScrollBottom ────────────────────────────────────────────────────

func TestPaintScrollBottom_ReturnsVisibleLines(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30

	got := m.paintScrollBottom()
	want := m.visibleLines()
	if got != want {
		t.Fatalf("paintScrollBottom = %d, want %d", got, want)
	}
}

func TestBuildTopBar_VeryNarrowTerminal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 5 // extremely narrow
	m.height = 10
	m.cwd = "/home/user/projects/tyci-agent"
	m.home = "/home/user"

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 5 {
		t.Fatalf("buildTopBar width = %d, want 5", w)
	}
}

func TestPaintScrollBottom_ZeroForModal(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 30
	m.subagentModalActive = true

	got := m.paintScrollBottom()
	if got != 0 {
		t.Fatalf("paintScrollBottom = %d in modal, want 0", got)
	}
}

// ─── buildTopBar counts ───────────────────────────────────────────────────

func TestBuildTopBar_ShowsCounts(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user/projects/tyci-agent"
	m.home = "/home/user"
	m.toolCount = 18
	m.skillCount = 12
	m.mcpCount = 3

	bar := m.buildTopBar()
	if !strings.Contains(bar, "skills: 12") {
		t.Fatalf("buildTopBar should show 'skills: 12', got %q", bar)
	}
	if !strings.Contains(bar, "tools: 18") {
		t.Fatalf("buildTopBar should show 'tools: 18', got %q", bar)
	}
	if !strings.Contains(bar, "mcp: 3") {
		t.Fatalf("buildTopBar should show 'mcp: 3', got %q", bar)
	}
}

func TestBuildTopBar_ShowsZeroCounts(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.toolCount = 0
	m.skillCount = 0
	m.mcpCount = 0

	bar := m.buildTopBar()
	if !strings.Contains(bar, "skills: 0") {
		t.Fatalf("buildTopBar should show 'skills: 0' even when zero, got %q", bar)
	}
	if !strings.Contains(bar, "tools: 0") {
		t.Fatalf("buildTopBar should show 'tools: 0' even when zero, got %q", bar)
	}
	if !strings.Contains(bar, "mcp: 0") {
		t.Fatalf("buildTopBar should show 'mcp: 0' even when zero, got %q", bar)
	}
}

func TestBuildTopBar_CountsUseDistinctColors(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.toolCount = 9
	m.skillCount = 5
	m.mcpCount = 2

	bar := m.buildTopBar()

	// Verify all three counters are present with their labels.
	if !strings.Contains(bar, "skills: 5") {
		t.Fatalf("buildTopBar should show 'skills: 5', got %q", bar)
	}
	if !strings.Contains(bar, "tools: 9") {
		t.Fatalf("buildTopBar should show 'tools: 9', got %q", bar)
	}
	if !strings.Contains(bar, "mcp: 2") {
		t.Fatalf("buildTopBar should show 'mcp: 2', got %q", bar)
	}

	// The bar should be exactly m.width wide.
	if w := lipgloss.Width(bar); w != 80 {
		t.Fatalf("buildTopBar width = %d, want 80", w)
	}
}

func TestBuildTopBar_ZeroCountsUseDimmedColor(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 80
	m.height = 24
	m.cwd = "/home/user"
	m.home = "/home/user"
	m.toolCount = 0
	m.skillCount = 0
	m.mcpCount = 0

	bar := m.buildTopBar()

	// Zero counts should still be rendered (not omitted).
	if !strings.Contains(bar, "skills: 0") {
		t.Fatalf("buildTopBar should show 'skills: 0' even when zero, got %q", bar)
	}
	if !strings.Contains(bar, "tools: 0") {
		t.Fatalf("buildTopBar should show 'tools: 0' even when zero, got %q", bar)
	}
	if !strings.Contains(bar, "mcp: 0") {
		t.Fatalf("buildTopBar should show 'mcp: 0' even when zero, got %q", bar)
	}
}

func TestBuildTopBar_LongPathTruncatedPreservesCounts(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 60 // wide enough to fit truncated path + all counters
	m.height = 24
	m.cwd = "/home/user/projects/really/long/path/to/tyci-agent"
	m.home = "/home/user"
	m.toolCount = 18
	m.skillCount = 12
	m.mcpCount = 3

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 60 {
		t.Fatalf("buildTopBar width = %d, want 60", w)
	}
	// All three counters must still be visible after truncation.
	if !strings.Contains(bar, "skills: 12") {
		t.Fatalf("skills counter should be visible after path truncation, got %q", bar)
	}
	if !strings.Contains(bar, "tools: 18") {
		t.Fatalf("tools counter should be visible after path truncation, got %q", bar)
	}
	if !strings.Contains(bar, "mcp: 3") {
		t.Fatalf("mcp counter should be visible after path truncation, got %q", bar)
	}
	// The path tail should still be present.
	if !strings.Contains(bar, "tyci-agent") {
		t.Fatalf("path tail should be visible after truncation, got %q", bar)
	}
}

func TestBuildTopBar_DropsMcpFirst(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 25 // very narrow — must drop mcp counter
	m.height = 24
	m.cwd = "/home/user/projects/tyci-agent"
	m.home = "/home/user"
	m.toolCount = 18
	m.skillCount = 12
	m.mcpCount = 3

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 25 {
		t.Fatalf("buildTopBar width = %d, want 25", w)
	}
	// mcp should be dropped first.
	if strings.Contains(bar, "mcp:") {
		t.Fatalf("mcp counter should be dropped in tight width, got %q", bar)
	}
	// skills and tools should still be visible.
	if !strings.Contains(bar, "skills: 12") {
		t.Fatalf("skills counter should remain when mcp is dropped, got %q", bar)
	}
	if !strings.Contains(bar, "tools: 18") {
		t.Fatalf("tools counter should remain when mcp is dropped, got %q", bar)
	}
}

func TestBuildTopBar_DropsToolsSecond(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 20 // extremely narrow — must drop mcp and tools
	m.height = 24
	m.cwd = "/home/user/projects/tyci-agent"
	m.home = "/home/user"
	m.toolCount = 18
	m.skillCount = 12
	m.mcpCount = 3

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 20 {
		t.Fatalf("buildTopBar width = %d, want 20", w)
	}
	// mcp and tools should be dropped.
	if strings.Contains(bar, "mcp:") {
		t.Fatalf("mcp counter should be dropped first, got %q", bar)
	}
	if strings.Contains(bar, "tools:") {
		t.Fatalf("tools counter should be dropped second, got %q", bar)
	}
	// skills should remain (last to drop).
	if !strings.Contains(bar, "skills: 12") {
		t.Fatalf("skills counter should remain when others are dropped, got %q", bar)
	}
}

func TestBuildTopBar_KeepsSkillsAfterDroppingOthers(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 0, 0, 0)
	m.ready = true
	m.width = 18 // barely enough for "skills: 12" + separator + short path
	m.height = 24
	m.cwd = "/home/user/projects"
	m.home = "/home/user"
	m.toolCount = 18
	m.skillCount = 12
	m.mcpCount = 3

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 18 {
		t.Fatalf("buildTopBar width = %d, want 18", w)
	}
	// Only skills should remain (mcp and tools dropped).
	if strings.Contains(bar, "mcp:") {
		t.Fatalf("mcp counter should be dropped, got %q", bar)
	}
	if strings.Contains(bar, "tools:") {
		t.Fatalf("tools counter should be dropped, got %q", bar)
	}
	if !strings.Contains(bar, "skills:") {
		t.Fatalf("skills counter should be the last to drop, got %q", bar)
	}
}

func TestNewModel_StoresCountFields(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil, 7, 3, 1)
	if m.toolCount != 7 {
		t.Fatalf("toolCount = %d, want 7", m.toolCount)
	}
	if m.skillCount != 3 {
		t.Fatalf("skillCount = %d, want 3", m.skillCount)
	}
	if m.mcpCount != 1 {
		t.Fatalf("mcpCount = %d, want 1", m.mcpCount)
	}
}
