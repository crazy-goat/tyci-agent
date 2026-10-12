package agent

import (
	"fmt"
	"regexp"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
)

// noticeTag matches the opening and the closing tag of a notice, in any case.
var noticeTag = regexp.MustCompile(`(?i)<(/?)tyci-notice`)

// lateNotice formats a late tool result as a tail notice. The notice is a
// user text block, never a toolResult block, so the model sees it as
// untrusted data. A tool result cannot open or close the tag early: both tags
// in the content are escaped.
func lateNotice(r bus.ToolResult) string {
	content := noticeTag.ReplaceAllString(r.Content, `<\${1}tyci-notice`)
	attrs := fmt.Sprintf("call_id=%s failed=%t duration_ms=%d truncated=%t", r.CallID, r.Failed, r.DurationMS, r.Truncated)
	if r.FullPath != "" {
		attrs += fmt.Sprintf(` full_path="%s"`, r.FullPath)
	}
	return fmt.Sprintf(`<tyci-notice kind=tool.result %s>%s</tyci-notice>`, attrs, content)
}

// appendLateNotices appends one user text block per late result, at the tail
// of the conversation. Earlier messages stay byte-identical, so the prompt
// cache prefix is kept.
func appendLateNotices(msgs *[]connector.Message, cfg Config, late []bus.ToolResult) {
	for _, r := range late {
		text := lateNotice(r)
		*msgs = append(*msgs, connector.Message{
			Role:    "user",
			Content: []connector.ContentBlock{{Type: "text", Text: text}},
		})
		if cfg.Session != nil {
			_ = cfg.Session.WriteMessage("user", []session.ContentBlock{{Type: "text", Text: text}}, nil)
		}
	}
}
