package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/internal/readline"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
	"golang.org/x/term"
)

// collector captures agent output (simplified for subagent runner)
type collector struct {
	text strings.Builder
}

// ensureLazySession opens a session file at sessionPath if it isn't already
// open, returning the (possibly freshly-opened) *session.Session and the path
// for downstream writes. It is the single entry point used by console, TUI
// and one-shot run to optionally recreate their session the moment we have a
// user prompt to write — rather than at startup, which would otherwise litter
// ~/.tyci/sessions/ with empty JSONL files for every repl a user opens
// without ever typing a prompt.
//
// Behavior:
//   - If sess is already non-nil (e.g. --session explicitly resumed an
//     existing file from disk), it is returned as-is.
//   - If sessionPath is empty or --no-session is the reason we have no path,
//     it returns (nil, "", nil) so callsites can fall through to
//     "no-session" mode without any extra plumbing.
//   - Otherwise the path is opened. A file that already exists on disk is
//     resumed (same as session.Open behavior); a fresh file is created with a
//     header, exactly as if the user had passed --session up front.
//
// Errors from session.Open are reported on stderr and the function returns
// (nil, "", nil) so callers disable persistence for this session rather than
// crashing the REPL.
func ensureLazySession(sess *session.Session, sessionPath, cwd, modelName, providerName string) (*session.Session, string, error) {
	if sess != nil {
		return sess, sessionPath, nil
	}
	if sessionPath == "" {
		return nil, "", nil
	}
	resolvedCWD := normalizeCWD(cwd)
	newSess, err := session.Open(sessionPath, resolvedCWD, modelName, providerName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session: %v (continuing without session)\n", err)
		return nil, "", nil
	}
	return newSess, sessionPath, nil
}

// normalizeCWD falls back to the current working directory if the supplied
// value is empty. Used by ensureLazySession so callers don't have to repeat
// the os.Getwd dance.
func normalizeCWD(cwd string) string {
	if cwd != "" {
		return cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func (c *collector) Thinking(text string)                           { c.text.WriteString(text) }
func (c *collector) Text(text string)                               { c.text.WriteString(text) }
func (c *collector) Request(string)                                 {}
func (c *collector) ToolCallStart(name string)                      {}
func (c *collector) ToolCallDelta(delta string)                     {}
func (c *collector) ToolCallEnd(name, result string)                {}
func (c *collector) ToolFinish()                                    {}
func (c *collector) ToolBlock(msg string)                           {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {}
func (c *collector) Total(usage stream.Usage)                       {}
func (c *collector) Error(err error)                                {}
func (c *collector) End()                                           {}

// toolsAdapter implements the tools.Runner interface by delegating to tools.RunTool.
type toolsAdapter struct{}

func (toolsAdapter) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		// Surface res.Truncated to the calling LLM as a stable, parseable
		// suffix marker. Without this, a parent that invokes the subagent
		// tool in single-task mode (the most common case) only sees the
		// "may be incomplete" wording inline and must guess at it. The
		// parallel-array path already encodes truncated per-item via
		// json.Marshal, so this only closes the single-task gap. The
		// marker literal is exported (tools.TruncatedMarker) so a test
		// in tools/ can lock the format down and drift is caught.
		if res.Truncated {
			return res.Content + "\n\n" + tools.TruncatedMarker, nil
		}
		return res.Content, nil
	}
	return "", fmt.Errorf("%s", res.Error)
}

var fallbackScanner *bufio.Scanner

// simplePrompt prompts the user for input using a basic scanner.
func simplePrompt(prompt string) (string, error) {
	if fallbackScanner == nil {
		fallbackScanner = bufio.NewScanner(os.Stdin)
	}
	fmt.Fprint(os.Stdout, prompt)
	if !fallbackScanner.Scan() {
		return "", readline.ErrEOF
	}
	return fallbackScanner.Text(), fallbackScanner.Err()
}

// watchESC watches for ESC key press to cancel the context.
func watchESC(cancel context.CancelFunc) func() {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}
	}

	oldState, err := term.GetState(fd)
	if err != nil {
		return func() {}
	}

	// Set raw mode (non-canonical, no echo, etc.)
	_, err = term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}

	// Tweak: keep ISIG (for Ctrl+C signals) and OPOST (output processing),
	// set VMIN=0 VTIME=1 so read() returns every 100ms instead of blocking forever.
	if err := applyTerminalTweaks(fd); err != nil {
		term.Restore(fd, oldState)
		return func() {}
	}

	stop := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		for {
			select {
			case <-stop:
				return
			default:
			}
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				// timeout or error — check if we should stop before retrying
				select {
				case <-stop:
					return
				default:
					continue
				}
			}
			if buf[0] == 0x1b { // ESC
				cancel()
				return
			}
			// Any other key is discarded
		}
	}()

	return func() {
		close(stop) // signal goroutine to stop
		term.Restore(fd, oldState)
		// Don't wait for goroutine — it will exit on next timeout or stop signal
	}
}

// summarizeResume renders a compact "loaded session …" info block on the
// active display. The conversation array itself is loaded elsewhere by
// session.RebuildMessages so the model still has the full history; this
// function intentionally does NOT shovel every event through
// Display.Text / Display.Thinking — doing so wrecks the TUI on long
// sessions: blocks scroll off-screen, glamour renders thousands of
// lines, selection rewrites the screen with stale ANSI, and PgUp/PgDown
// become useless because there is nothing left to overflow into.
//
// Output shape: a single ToolBlock line that the user can read at a glance:
//
//	"📋 Resumed session a1b2… (42 messages, 12345 in / 6789 out tokens).
//	 Last user: <first 80 chars>. Last assistant: <first 80 chars>."
func summarizeResume(disp display.Display, sessID string, msgs []providers.RichMessage, total session.TotalUsage, corruptCount int) {
	if disp == nil {
		return
	}

	// Walk the conversation backwards to find the last user message and
	// the last assistant message with text (ignore huge thinking blocks
	// for the snippet — the model can still see them in `msgs`).
	var lastUser, lastAssistantText string
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		switch m.Role {
		case "user":
			if lastUser == "" {
				for _, b := range m.Content {
					if b.Type == "text" && b.Text != "" {
						lastUser = b.Text
					}
				}
			}
		case "assistant":
			if lastAssistantText == "" {
				for _, b := range m.Content {
					if b.Type == "text" && b.Text != "" {
						lastAssistantText = b.Text
					}
				}
			}
		}
		if lastUser != "" && lastAssistantText != "" {
			break
		}
	}

	disp.ToolBlock(buildResumeSummary(sessID, len(msgs), total, lastUser, lastAssistantText, corruptCount))
	disp.End()
}

// buildResumeSummary formats a single info line for summarizeResume. Kept
// separate so callers can also format the line into stderr/logs without
// routing through the Display interface (used by tests and one-shot prompt
// mode where the user does want to see a copy on stderr too).
func buildResumeSummary(sessID string, msgCount int, total session.TotalUsage, lastUser, lastAssistant string, corruptCount int) string {
	var b strings.Builder
	b.WriteString("📋 Resumed session ")
	b.WriteString(sessID)
	b.WriteString(" (")
	b.WriteString(strconv.Itoa(msgCount))
	b.WriteString(" messages, ")
	b.WriteString(strconv.Itoa(total.Input))
	b.WriteString(" in / ")
	b.WriteString(strconv.Itoa(total.Output))
	b.WriteString(" out tokens")
	if total.TotalCost > 0 {
		b.WriteString(", $")
		b.WriteString(strconv.FormatFloat(total.TotalCost, 'f', 4, 64))
		b.WriteString(" total")
	}
	if corruptCount > 0 {
		b.WriteString(", ")
		b.WriteString(strconv.Itoa(corruptCount))
		b.WriteString(" corrupt lines skipped")
	}
	b.WriteString(")")
	if lastUser != "" {
		b.WriteString("\nLast user: ")
		b.WriteString(truncateForSummary(lastUser, 80))
	}
	if lastAssistant != "" {
		b.WriteString("\nLast assistant: ")
		b.WriteString(truncateForSummary(lastAssistant, 80))
	}
	b.WriteString("\n▶ Continuing from session end")
	return b.String()
}

// truncateForSummary returns s with at most maxLen runes, appending "…"
// when truncated. Used to keep the resume summary readable in one or two
// screen lines regardless of how chatty the last turn was.
func truncateForSummary(s string, maxLen int) string {
	if maxLen <= 0 || s == "" {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

// replaySessionToDisplay reads a JSONL session file and re-renders every
// message into the active display as a small number of stable, scannable
// blocks. It deliberately avoids Display.Text / Display.Thinking /
// Display.ToolCallStart: those paths route through glamour + streaming
// wrappers and break three things at once on long sessions:
//
//  1. PgUp/PgDown — glamour keeps mutating cachedLines/block state while
//     the user is mid-scroll, so the anchor scrolls under the cursor.
//  2. Mouse selection — selection.Y points into a renderBuffer that got
//     rebuilt under the user (glamour runs on dirty blocks between events),
//     so releasing the mouse puts highlighted cells onto rows that now
//     contain the next message's first line. The screen "blanks" because
//     the selection highlight spans ghost text that no longer exists.
//  3. Performance — a 200-message session produces 1500+ dirty blocks,
//     each running through glamour twice (once streamed, once re-rendered
//     on block boundary). CPU becomes unusable and memory balloons.
//
// Instead, every message becomes one ToolBlock (kind="block") rendered by
// renderErrorOrBlock — pure wrapText, no glamour, deterministic line
// counts. Multiple consecutive blocks are stable so scrolling is predictable
// and selection highlights stay anchored to the rows they were drawn on.
//
// To keep the transcript reasonable even on huge sessions, maxReplayBlocks
// caps the number of replayed message blocks; older messages are folded
// into a single "earlier history collapsed" info block and the full
// conversation is still loaded into the model (session.RebuildMessages).
func replaySessionToDisplay(disp display.Display, sessionPath string) {
	if disp == nil {
		return
	}
	f, err := os.Open(sessionPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot replay session: %v\n", err)
		return
	}
	defer f.Close()

	// Walk JSONL, build one formatted block per message, then push them.
	type replayEntry struct {
		role string
		body string // formatted, multi-line ready for ToolBlock
	}
	var entries []replayEntry

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		evType, _ := raw["type"].(string)
		if evType == "session" || evType == "session_end" || evType == "compaction" {
			continue
		}
		msgRaw, ok := raw["message"].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msgRaw["role"].(string)
		body := formatMessageForReplay(role, msgRaw)
		if body == "" {
			continue
		}
		entries = append(entries, replayEntry{role: role, body: body})
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: session replay error: %v\n", err)
	}

	// Cap the visible transcript. Older messages are still in `msgs` for
	// the model; we just don't crowd the screen.
	const maxReplayBlocks = 100
	if len(entries) > maxReplayBlocks {
		dropped := len(entries) - maxReplayBlocks
		disp.ToolBlock(fmt.Sprintf("… %d earlier messages collapsed (still loaded into model context) …", dropped))
		disp.End()
		entries = entries[len(entries)-maxReplayBlocks:]
	}

	for _, e := range entries {
		disp.ToolBlock(e.body)
		disp.End()
	}
	disp.ToolBlock("▶ Continuing from session end")
	disp.End()
}

// formatMessageForReplay turns one JSONL message event into a multi-line
// string suitable for a single display.ToolBlock (=> kind="block" =>
// renderErrorOrBlock). It collapses thinking to a single one-liner, merges
// user text fragments, and summarises tool calls / results so the user can
// scroll and select the transcript without glamour mangling every line.
func formatMessageForReplay(role string, msgRaw map[string]any) string {
	content, _ := msgRaw["content"].([]any)

	var sb strings.Builder
	switch role {
	case "user":
		sb.WriteString("[You]\n")
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			if txt, _ := b["text"].(string); txt != "" {
				sb.WriteString(txt)
				if !strings.HasSuffix(txt, "\n") {
					sb.WriteString("\n")
				}
			}
		}

	case "assistant":
		// Order: thinking (summary), text blocks, tool call summaries.
		var thinkingBytes int
		var thinkingLines int
		var textParts []string
		var toolCalls []string
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			bType, _ := b["type"].(string)
			switch bType {
			case "thinking":
				if txt, _ := b["thinking"].(string); txt != "" {
					thinkingBytes += len(txt)
					thinkingLines += strings.Count(txt, "\n") + 1
				}
			case "text":
				if txt, _ := b["text"].(string); txt != "" {
					textParts = append(textParts, txt)
				}
			case "toolCall":
				name, _ := b["name"].(string)
				args, _ := b["arguments"].(string)
				// Compact JSON: trim, drop trailing whitespace.
				args = strings.TrimSpace(args)
				if args == "" {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s()", name))
				} else if len(args) > 120 {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s(%s…)", name, args[:120]))
				} else {
					toolCalls = append(toolCalls, fmt.Sprintf("- tool %s(%s)", name, args))
				}
			}
		}
		if thinkingBytes > 0 {
			fmt.Fprintf(&sb, "[Assistant thinking: %d chars / %d lines — collapsed]\n", thinkingBytes, thinkingLines)
		}
		if len(textParts) > 0 {
			sb.WriteString("[Assistant]\n")
			for _, t := range textParts {
				sb.WriteString(t)
				if !strings.HasSuffix(t, "\n") {
					sb.WriteString("\n")
				}
			}
		}
		if len(toolCalls) > 0 {
			sb.WriteString("[Assistant tool calls]\n")
			sb.WriteString(strings.Join(toolCalls, "\n"))
			sb.WriteString("\n")
		}
		if thinkingBytes == 0 && len(textParts) == 0 && len(toolCalls) == 0 {
			return ""
		}

	case "toolResult", "tool":
		// Identify which tool produced this result, then show the result
		// text (truncated). Multiple tool results land in separate entries,
		// one each, so each is scrollable / selectable on its own.
		var toolName string
		var resultParts []string
		for _, cb := range content {
			b, ok := cb.(map[string]any)
			if !ok {
				continue
			}
			bType, _ := b["type"].(string)
			if bType != "text" {
				continue
			}
			if toolName == "" {
				toolName, _ = b["toolName"].(string)
			}
			if txt, _ := b["text"].(string); txt != "" {
				resultParts = append(resultParts, txt)
			}
		}
		if toolName == "" {
			toolName = "tool"
		}
		header := fmt.Sprintf("[Tool result: %s]\n", toolName)
		sb.WriteString(header)
		// Body lines — small-to-medium results pass through verbatim so the
		// user can see actual output. Huge results are truncated to keep
		// the transcript scrollable; the full text is still in the
		// session file for /resume-list debugging if needed.
		const maxToolResultChars = 4000
		body := strings.Join(resultParts, "\n")
		if len(body) > maxToolResultChars {
			body = body[:maxToolResultChars] + fmt.Sprintf("\n… (truncated, %d more chars)", len(body)-maxToolResultChars)
		}
		sb.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			sb.WriteString("\n")
		}

	default:
		return ""
	}

	out := sb.String()
	// Drop trailing blank lines so the block doesn't render as a one-line
	// padded spacer — keeps the message-region line counts predictable,
	// which is exactly what makes scroll math stable in the long-session
	// case.
	return strings.TrimRight(out, "\n")
}

// parseUsageFromMap extracts stream.Usage from a JSON map.
func parseUsageFromMap(u map[string]any) stream.Usage {
	var us stream.Usage
	if v, ok := u["input"].(float64); ok {
		us.Input = int(v)
	}
	if v, ok := u["output"].(float64); ok {
		us.Output = int(v)
	}
	if v, ok := u["reasoning"].(float64); ok {
		us.Reasoning = int(v)
	}
	if v, ok := u["cacheRead"].(float64); ok {
		us.CacheRead = int(v)
	}
	if v, ok := u["cacheWrite"].(float64); ok {
		us.CacheWrite = int(v)
	}
	return us
}
