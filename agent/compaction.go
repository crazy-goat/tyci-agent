package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
)

// compactKeepMessages is how many of the newest messages survive a compaction.
const compactKeepMessages = 8

// compactSummaryTimeout bounds the one extra model call that writes the
// summary before a harness-triggered compaction. When it expires, the
// compaction uses the fixed marker instead and the turn goes on.
const compactSummaryTimeout = 60 * time.Second

// compactSummaryMaxTokens caps the reply of the summary call. It is sent as
// MaxTokens, so a long summary is cut rather than running on. A window with
// less room gets a lower cap (see summaryBudget).
const compactSummaryMaxTokens = 6144

// compactSummaryMinTokens is the least reply the summary call asks for. With
// less room than this, the call is not made and the fixed marker is used.
const compactSummaryMinTokens = 1024

// compactSummaryOverhead is the room kept for the summary instruction and the
// transcript labels, which the summary call adds on top of the conversation.
const compactSummaryOverhead = 2048

// compactSummaryReserve is the most of the context window that the hard limit
// leaves free for the summary call. The reserve is the smaller of this value
// and a tenth of the window (see compactThresholds). A tenth, not a fifth,
// keeps the hard limit above the 80% soft default, so the soft notice still
// shows for small windows.
const compactSummaryReserve = 8192

// summaryBudget returns the reply cap of the summary call for a conversation
// of used tokens in a window: the room left after the conversation and the
// instruction overhead, at most compactSummaryMaxTokens.
//
// Assumption: used is the last input plus output count. It includes the
// system prompt and the tool schemas, which the summary call does not send.
// The call sends only the transcript and the instruction. Counting the
// system prompt and the tools again makes the room smaller, so the estimate
// is conservative and is not reduced further. An unknown window (0) keeps
// compactSummaryMaxTokens, because the provider decides then.
func summaryBudget(used, window int) int {
	if window <= 0 {
		return compactSummaryMaxTokens
	}
	return min(compactSummaryMaxTokens, window-used-compactSummaryOverhead)
}

// compactSummaryInstruction is the fixed request sent with the conversation
// when the harness compacts. It asks for plain text, so no tool is needed.
const compactSummaryInstruction = `Write a summary of the conversation above. The earlier messages are removed after this summary, so the summary must let the assistant continue the work without them.

Use plain text. Do not call tools. Do not add anything that is not in the conversation. Keep exact paths, names and identifiers.

Cover these points, in this order:
1. Facts and decisions.
2. Files and paths touched.
3. Work done.
4. Unfinished work and next steps.
5. Open questions.
6. Anything the user asked to keep.`

// compactionSink is an optional Sink capability: a sink that shows the
// divider of a compaction. It is optional for the same reason as PhaseSink
// (run_once.go). Sinks that implement it include the sink of a subagent
// (streamingCollector), the ledger wrapper that forwards to it, and display.TUI.
type compactionSink interface {
	Compaction(meta session.CompactMeta)
}

// emitCompaction tells d about a compaction, when d shows dividers.
func emitCompaction(d Sink, meta session.CompactMeta) {
	if cs, ok := d.(compactionSink); ok {
		cs.Compaction(meta)
	}
}

// CompactSession is the default compactor used by top-level conductors. meta
// describes the compaction and is stored with its session event.
func CompactSession(sess *session.Session, msgs *[]connector.Message, summary, focus string, meta session.CompactMeta) (string, error) {
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
	path, err := sess.Compact(summary, tailID, keep, dropped, meta)
	if err != nil {
		return "", err
	}
	*msgs = compacted
	return path, nil
}

// compactInMemory replaces msgs with task, a user message with note, and
// the tail of msgs. It is the compaction of an agent without a session
// (Config.InLoopCompaction). task may be nil. When the tail already holds
// task, task is not copied again. It returns false and changes nothing when
// there is nothing to drop.
func compactInMemory(msgs *[]connector.Message, task *connector.Message, note string) bool {
	tail := compactTail(*msgs)
	if len(tail) == len(*msgs) {
		return false
	}
	var head []connector.Message
	if task != nil && !slices.ContainsFunc(tail, func(m connector.Message) bool { return reflect.DeepEqual(m, *task) }) {
		head = append(head, *task)
	}
	head = append(head, connector.Message{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: note}}})
	*msgs = session.SanitizeMessageSequence(append(head, tail...))
	return true
}

func compactTail(msgs []connector.Message) []connector.Message {
	if len(msgs) <= compactKeepMessages {
		return append([]connector.Message(nil), msgs...)
	}
	return append([]connector.Message(nil), msgs[len(msgs)-compactKeepMessages:]...)
}

// summaryResult is what one summary call produced.
type summaryResult struct {
	text  string
	usage stream.Usage
	stats stream.Stats
}

// summaryCall carries the outcome of runSummaryCall over a channel.
type summaryCall struct {
	res summaryResult
	err error
}

// errEmptySummary is returned when the summary call ends without text.
var errEmptySummary = errors.New("compaction summary is empty")

// summarizeForCompaction asks mc for a summary of msgs, with no tools, and
// returns after timeout at the latest. The caller falls back to a fixed
// marker on any error. The usage of the call is returned even when the
// summary is empty, so the caller can count it; on a timeout the usage is
// lost, because the call has not finished.
func summarizeForCompaction(ctx context.Context, mc connector.ModelClient, msgs []connector.Message, timeout time.Duration, maxTokens int) (summaryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan summaryCall, 1)
	go func() {
		res, err := runSummaryCall(ctx, mc, msgs, maxTokens)
		done <- summaryCall{res: res, err: err}
	}()
	select {
	case c := <-done:
		return c.res, c.err
	case <-ctx.Done():
		return summaryResult{}, ctx.Err()
	}
}

// runSummaryCall sends the conversation as one text transcript, not as the
// raw messages. Anthropic rejects tool blocks in a request that defines no
// tools, and this call defines none. Thinking blocks are left out.
func runSummaryCall(ctx context.Context, mc connector.ModelClient, msgs []connector.Message, maxTokens int) (summaryResult, error) {
	start := time.Now()
	req := connector.Request{
		Model: mc.Model(),
		Messages: []connector.Message{{
			Role:    "user",
			Content: []connector.ContentBlock{{Type: "text", Text: transcriptForSummary(msgs) + "\n" + compactSummaryInstruction}},
		}},
		MaxTokens: maxTokens,
		// The request prefix differs from the conversation, so a cache
		// entry would not be reused. Writing one would cost extra.
		NoPromptCache: true,
	}
	events, err := mc.Stream(ctx, req)
	if err != nil {
		return summaryResult{}, err
	}
	var res summaryResult
	var text strings.Builder
	// Drain the whole channel, also after an error, so the provider never
	// blocks on a send.
	for ev := range events {
		switch e := ev.(type) {
		case stream.TextDelta:
			text.WriteString(e.Text)
		case stream.Finish:
			res.usage = e.Usage
		case stream.StreamError:
			err = e.Err
		}
	}
	res.stats = stream.Stats{Duration: time.Since(start)}
	res.text = strings.TrimSpace(text.String())
	if err != nil {
		return res, err
	}
	if res.text == "" {
		return res, errEmptySummary
	}
	return res, nil
}

// transcriptForSummary renders msgs as plain text, one block per message
// role, for the summary call.
func transcriptForSummary(msgs []connector.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "[%s]\n", m.Role)
		for _, c := range m.Content {
			switch {
			case c.Type == "thinking":
				continue
			case c.Type == "toolCall":
				fmt.Fprintf(&b, "tool call %s %s\n", c.Name, c.Arguments)
			case c.Text != "":
				b.WriteString(c.Text + "\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}
