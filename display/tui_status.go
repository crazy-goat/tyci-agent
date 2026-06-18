package display

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m TuiModel) buildStatus() string {
	leftParts := []string{}
	rightParts := []string{}

	if m.modelName != "" {
		leftParts = append(leftParts, m.modelName)
	}

	if m.scrollLine > 0 {
		leftParts = append(leftParts, fmt.Sprintf("↑%d lines", m.scrollLine))
	}

	if m.statusMessage != "" {
		leftParts = append(leftParts, m.statusMessage)
	}

	if !m.reading {
		switch m.status {
		case "thinking":
			leftParts = append(leftParts, "⟳ thinking...")
		case "responding":
			leftParts = append(leftParts, "⟳ responding...")
		case "tool":
			leftParts = append(leftParts, "⟳ tool...")
		default:
			leftParts = append(leftParts, "⟳ working...")
		}
	}

	if m.lastUsage.Input > 0 || m.lastUsage.Output > 0 {
		inNew := m.lastUsage.Input - m.lastUsage.CacheRead
		if inNew < 0 {
			inNew = 0
		}
		u := fmt.Sprintf("in=%d", inNew)
		if m.lastUsage.CacheRead > 0 {
			u += fmt.Sprintf(" (+%d cache)", m.lastUsage.CacheRead)
		}
		u += fmt.Sprintf(" out=%d", m.lastUsage.Output)
		if m.lastUsage.Reasoning > 0 {
			u += fmt.Sprintf(" r=%d", m.lastUsage.Reasoning)
		}
		if m.lastUsage.CacheWrite > 0 {
			u += fmt.Sprintf(" cache_w=%d", m.lastUsage.CacheWrite)
		}
		u += fmt.Sprintf(" ctx=%d", m.lastUsage.Input+m.lastUsage.Output)
		genDur := m.lastStats.Duration - m.lastStats.FirstToken
		if genDur < 0 {
			genDur = 0
		}
		u += fmt.Sprintf(" t=%.1fs ttft=%.2fs tok/s=%s",
			m.lastStats.Duration.Seconds(),
			m.lastStats.FirstToken.Seconds(),
			fmtRate(m.lastUsage.Output, genDur),
		)
		rightParts = append(rightParts, u)
	}

	if len(leftParts) == 0 && len(rightParts) == 0 {
		return ""
	}

	left := strings.Join(leftParts, " │ ")
	right := strings.Join(rightParts, " │ ")

	// Right-align the right part
	leftW := lipgloss.Width(left)
	rightW := lipgloss.Width(right)
	padding := m.width - leftW - rightW
	if padding < 1 {
		padding = 1
	}
	return " " + left + strings.Repeat(" ", padding-1) + right
}

// ─── Public API ───────────────────────────────────────────────────────────
