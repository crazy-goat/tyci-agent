package conductor

import "strings"

// agentMentionPrefix is what the TUI's "@" popup inserts when a named agent
// is picked (see display/tui_filecomplete.go's acceptFileComplete). It is
// duplicated here as a literal rather than imported, deliberately: display
// depends on conductor's neighbours already (agent, connector), and a shared
// dependency the other way would be a needless coupling for one string that
// is part of the wire format between the two, not an implementation detail
// of either.
const agentMentionPrefix = "@agent:"

// extractAgentMentions finds every "@agent:<name>" token in prompt and
// returns the named agents, deduplicated and in first-seen order.
//
// Matching is done on whitespace-delimited tokens rather than a regexp: it
// is exactly what fileCompleteToken already guarantees on the way in (the
// tag starts a token, ends at the next space), and it means a trailing
// space inserted by the popup or a sentence continuing right after do not
// need special-casing here. Trailing punctuation (a comma or period ending
// the sentence) is stripped so "...continue with @agent:reviewer." names
// "reviewer", not "reviewer.".
func extractAgentMentions(prompt string) []string {
	var names []string
	seen := make(map[string]bool)
	for _, tok := range strings.Fields(prompt) {
		if !strings.HasPrefix(tok, agentMentionPrefix) {
			continue
		}
		name := strings.TrimPrefix(tok, agentMentionPrefix)
		name = strings.TrimRight(name, ".,;:!?)")
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// buildAgentMentionNote produces the harness-authored instruction appended
// to a submitted message that named one or more agents via "@agent:<name>".
//
// This is a per-message note, not a periodic reminder like
// agent.buildTodoReminder/buildJobReminder — it rides along with the one
// user message that named the agent(s), as a second content block, rather
// than being injected as its own turn later. Framed as automated and not
// the user's own words, same convention as those reminders.
func buildAgentMentionNote(names []string) string {
	var b strings.Builder
	b.WriteString("[automated note, not the user] The message above named the following agent(s) via @agent:<name>: ")
	for i, name := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(name)
	}
	b.WriteString(". Continue the relevant part of this request in a subagent call (the `subagent` tool) with its `agent` field set to that name — that definition (from .tyci/agents/) supplies the child's system prompt and, where set, its model and tools. An unknown name is a real error from the tool, not something to silently fall back from.")
	return b.String()
}
