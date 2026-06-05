package display

import (
	"fmt"
	"time"

	"github.com/decodo/tyci-agent/stream"
)

func fmtDur(d time.Duration) string {
	return fmt.Sprintf("%.2fs", d.Seconds())
}

func fmtRate(tokens int, genDur time.Duration) string {
	if genDur <= 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", float64(tokens)/genDur.Seconds())
}

func buildUsageLine(fresh, out, cacheRead, cacheWrite int, stats stream.Stats) string {
	parts := fmt.Sprintf("Usage: in=%d out=%d cache_rd=%d cache_wr=%d", fresh, out, cacheRead, cacheWrite)
	parts += fmt.Sprintf(" dur=%s ttft=%s tok/s=%s",
		fmtDur(stats.Duration),
		fmtDur(stats.FirstToken),
		fmtRate(out, stats.Duration-stats.FirstToken),
	)
	return parts
}
