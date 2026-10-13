package agent

import "strings"

// NoticePrefix starts a line that the bus delivers as a notice, not as a line
// a person typed. The root package writes every such line with it. Only the
// tag for an ask reply differs, see replyPrefix.
const NoticePrefix = "[notice from="

// replyPrefix starts the tag of an ask reply, which the bus writes itself.
const replyPrefix = "<tyci-notice "

// IsNoticeLine reports whether a drained line is a notice from the harness
// rather than a line a person typed.
func IsNoticeLine(line string) bool {
	return strings.HasPrefix(line, NoticePrefix) || strings.HasPrefix(line, replyPrefix)
}
