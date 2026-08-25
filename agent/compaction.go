package agent

import (
	"fmt"
	"strings"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/session"
)

// CompactSession is the default compactor used by top-level conductors.
func CompactSession(sess *session.Session, msgs *[]connector.Message, summary, focus string) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("no writable session")
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("compaction summary must not be empty")
	}
	if focus = strings.TrimSpace(focus); focus != "" {
		summary += "\n\nPreserve this focus: " + focus
	}
	keep := compactTail(*msgs)
	// Keep a trailing assistant tool call when compact is itself the active
	// tool. executeAndAppendToolResults appends its matching result immediately
	// afterwards; dropping the call here would create an orphan result in both
	// live history and replay. SanitizeMessageSequence still removes orphan
	// results from a tail that starts mid-call.
	compacted := append([]connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: summary}}}}, keep...)
	compacted = session.SanitizeMessageSequence(compacted)
	if len(compacted) > 0 {
		keep = compacted[1:]
	} else {
		keep = nil
	}
	dropped := len(*msgs) - len(keep)
	// There is no persisted event id for a live in-memory boundary. Leaving
	// this empty is honest; consumers must not mistake a synthetic value for
	// an event they can fork at.
	tailID := ""
	path, err := sess.Compact(summary, tailID, keep, dropped)
	if err != nil {
		return "", err
	}
	*msgs = compacted
	return path, nil
}

func compactTail(msgs []connector.Message) []connector.Message {
	const keepMessages = 8
	if len(msgs) <= keepMessages {
		return append([]connector.Message(nil), msgs...)
	}
	return append([]connector.Message(nil), msgs[len(msgs)-keepMessages:]...)
}
