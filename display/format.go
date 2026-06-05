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

func buildUsageLine(usage stream.Usage, stats stream.Stats) string {
	newIn := usage.Input - usage.CacheRead
	if newIn < 0 {
		newIn = 0
	}
	parts := fmt.Sprintf("in=%d out=%d", newIn, usage.Output)
	if usage.Reasoning > 0 {
		parts += fmt.Sprintf(" r=%d", usage.Reasoning)
	}
	if usage.CacheRead > 0 || usage.CacheWrite > 0 {
		parts += fmt.Sprintf(" cr=%d cw=%d", usage.CacheRead, usage.CacheWrite)
	}
	genDur := stats.Duration - stats.FirstToken
	if genDur < 0 {
		genDur = 0
	}
	parts += fmt.Sprintf(" dur=%s ttft=%s tok/s=%s",
		fmtDur(stats.Duration),
		fmtDur(stats.FirstToken),
		fmtRate(usage.Output, genDur),
	)
	return parts
}
