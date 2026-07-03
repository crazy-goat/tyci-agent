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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 30 // narrow terminal
	m.height = 24
	m.cwd = "/home/user/projects/really/long/path/to/tyci-agent"
	m.home = "/home/user"

	bar := m.buildTopBar()
	w := lipgloss.Width(bar)
	if w != 30 {
		t.Fatalf("buildTopBar width = %d, want 30", w)
	}
	// Should still contain the tail of the path.
	if !strings.Contains(bar, "tyci-agent") {
		t.Fatalf("buildTopBar should keep tail of path, got %q", bar)
	}
}

func TestBuildTopBar_ShowsPath(t *testing.T) {
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
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
	m := newModel(nil, "test/model", "", []string{"test/model"}, nil, nil, nil, nil, nil, "", nil)
	m.ready = true
	m.width = 80
	m.height = 30
	m.subagentModalActive = true

	got := m.paintScrollBottom()
	if got != 0 {
		t.Fatalf("paintScrollBottom = %d in modal, want 0", got)
	}
}
