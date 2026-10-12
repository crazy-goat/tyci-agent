package display

import (
	"fmt"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// BenchmarkViewTasksTab measures one View() with the Tasks tab open, a
// 200-block transcript and 1, 5 or 20 running subagents (#636). The pricing
// catalog is a fixture under a temporary HOME, so the result does not depend
// on the developer's own catalog.
func BenchmarkViewTasksTab(b *testing.B) {
	for _, agents := range []int{1, 5, 20} {
		b.Run(fmt.Sprintf("agents=%d", agents), func(b *testing.B) {
			ledger.Reset()
			b.Cleanup(ledger.Reset)
			m := newWheelBenchModel(b, 200)
			for i := 0; i < agents; i++ {
				m.applyJobUpdate(jobs.Job{
					ID:          fmt.Sprintf("sub-bench-%d", i),
					Kind:        jobs.KindSubagent,
					Status:      jobs.StatusRunning,
					Description: fmt.Sprintf("worker %d", i),
					StartedAt:   time.Now(),
				})
			}
			m.openSidebar(sidebarTabTasks)
			_ = m.View()
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				_ = m.View()
			}
		})
	}
}
