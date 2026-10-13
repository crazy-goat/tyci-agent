package display

import (
	"errors"
	"io/fs"

	"github.com/crazy-goat/tyci-agent/session"
)

// loadAgentLog returns the lines to show for the agent log at path. Only the
// redacted file is read. An agent with no path (an ad-hoc agent, or a run
// without a run directory) and a missing file both give "(no log yet)". Any
// other read error gives "(log unavailable: <err>)". A last line that is still
// being written is skipped by the session reader.
func loadAgentLog(path string) []string {
	if path == "" {
		return []string{"(no log yet)"}
	}
	_, msgs, _, _, err := session.LoadForReplay(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"(no log yet)"}
	}
	if err != nil {
		return []string{"(log unavailable: " + sanitizeUntrusted(err.Error()) + ")"}
	}
	return formatAgentLogLines(msgs)
}
