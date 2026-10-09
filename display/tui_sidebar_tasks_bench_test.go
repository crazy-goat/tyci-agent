package display

import (
	"fmt"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// benchTasksModel returns a model with the Tasks tab open, 30 subagents,
// 100 Bash jobs and 20 Lua runs. The Lua history is restored when the
// benchmark ends.
func benchTasksModel(b testing.TB) TuiModel {
	b.Helper()
	m := newTestModelForSidebar()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 30; i++ {
		status := jobs.StatusDone
		if i%3 == 0 {
			status = jobs.StatusRunning
		}
		m.applyJobUpdate(jobs.Job{
			ID:          fmt.Sprintf("sub-%02d", i),
			Kind:        jobs.KindSubagent,
			Status:      status,
			Description: fmt.Sprintf("task/coder-%02d", i),
			StartedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}
	for i := 0; i < 100; i++ {
		status := jobs.StatusDone
		if i%10 == 0 {
			status = jobs.StatusRunning
		}
		m.applyJobUpdate(jobs.Job{
			ID:          fmt.Sprintf("bash-%03d", i),
			Kind:        jobs.KindBash,
			Status:      status,
			Description: fmt.Sprintf("go test ./pkg%03d/...", i),
			StartedAt:   base.Add(time.Duration(i) * time.Second),
		})
	}
	runs := make([]tools.LuaRun, 0, 20)
	for i := 0; i < 20; i++ {
		runs = append(runs, tools.LuaRun{
			Name:      fmt.Sprintf("script_%02d", i),
			StartedAt: base.Add(time.Duration(i) * time.Second),
			Duration:  time.Duration(i+1) * 37 * time.Millisecond,
			Success:   i%4 != 0,
		})
	}
	b.Cleanup(tools.SetLuaRunHistoryForTesting(runs))
	m.openSidebar(sidebarTabTasks)
	m.sidebarCursor = 5
	return m
}

// BenchmarkRenderSidebarTasks measures one render of the Tasks tab content.
func BenchmarkRenderSidebarTasks(b *testing.B) {
	m := benchTasksModel(b)
	width := m.sidebarLayout().contentWidth
	b.ResetTimer()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		_ = m.sidebarBodyLines(m.sidebarLayout(), width)
	}
}

// BenchmarkSidebarTasksStreaming measures a full View while a main chat answer
// streams and the Tasks tab is open.
func BenchmarkSidebarTasksStreaming(b *testing.B) {
	m := benchTasksModel(b)
	b.ResetTimer()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		m.handleBlockMsg(tuiMsgBlock{kind: "text", content: " new token"})
		_ = m.View()
	}
}
