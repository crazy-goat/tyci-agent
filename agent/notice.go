package agent

import "strings"

// NoticePrefix starts a line that the bus delivers as a notice, not as a line
// a person typed. The root package writes every such line with it. Only the
// tag for an ask reply differs, see ReplyPrefix.
const NoticePrefix = "[notice from="

// ReplyPrefix starts the tag of a notice from the harness that the bus writes
// itself, such as an ask reply. The bus adds the kind and the rest of the tag.
const ReplyPrefix = "<tyci-notice "

// IsNoticeLine reports whether a drained line is a notice from the harness
// rather than a line a person typed.
func IsNoticeLine(line string) bool {
	return strings.HasPrefix(line, NoticePrefix) || strings.HasPrefix(line, ReplyPrefix)
}
