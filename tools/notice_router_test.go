package tools

import "fmt"

// Production routes completion notices through the bus, which main wires
// with SetNoticePublisher. The tests in this package record notices on the
// fake JobNotifier and JobMailbox instead, so this router sends them there:
// to the mailbox of a parent that accepts them, and otherwise to the notifier
// with the tag that the bus gives a notice for a finished agent. The routing
// rules of the bus are tested in the bus package and in main.
func init() {
	SetNoticePublisher(routeTestNotice)
}

// routeTestNotice is the test publisher. A quiet flag has no effect here, the
// tests that check it install their own publisher.
func routeTestNotice(parentID, text string, _ bool) {
	if parentID != "" {
		if mb := getJobMailbox(); mb != nil && mb.Post(parentID, text) {
			return
		}
		text = fmt.Sprintf("[for agent %s, which has already finished — forwarded here instead] %s", parentID, text)
	}
	if n, ok := getJobNotifier().(*recordingNotifier); ok {
		n.Notify(text)
	}
}
