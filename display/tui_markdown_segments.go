package display

import "strings"

// mdStreamState tracks, per streaming text block, how much of the block's
// content is safe to glamour-render progressively.
//
// The safe-boundary rule: a "\n\n" (a blank line) is safe to flush on iff no
// fenced code block is open at that point. Nothing else needs tracking:
//
//   - GFM tables need no state — a table row is a single line and a table
//     ends at the first line that is not a row, so a blank line is always
//     already past the end of any table.
//   - Fences are the only construct spanning blank lines with no
//     single-line terminator, so their open/close state is the only thing
//     that must be carried across scans.
//
// Front matter and setext headings are deliberately not tracked — see TODO
// item 51's design notes. Any imperfection here self-heals:
// forceRenderDirtyBlocks re-renders the whole block from scratch once it
// finishes.
type mdStreamState struct {
	scanPos     int  // bytes of content already scanned for boundaries/fences
	inFence     bool // true while a fenced code block is open
	fenceMarker byte // '`' or '~' of the currently open fence
	fenceLen    int  // opening run length of the currently open fence
	fenceIndent int  // leading-whitespace count of the currently open fence's opening line

	safeUpto     int // byte offset of the latest known-safe flush point
	renderedUpto int // byte offset up to which content has been glamour-rendered

	// renderedPrefixLines holds the glamour-rendered, already-wrapped lines
	// for content[:renderedUpto]. Stable once written; never re-rendered.
	renderedPrefixLines []string

	// renderedPrefixJoined caches strings.Join(renderedPrefixLines, "\n").
	// It is rebuilt incrementally — once per flush, appending only the newly
	// flushed segment — rather than by re-joining the whole (potentially
	// large, ANSI-heavy) prefix on every streamed token. See
	// renderStreamingMarkdown's doc comment (F1 in the item-51 review).
	renderedPrefixJoined string
}

// fenceLineInfo inspects a single logical line (no trailing '\n') for fence
// marker syntax: any amount of leading whitespace, then a run of 3+
// identical '`' or '~' characters. Leading whitespace is deliberately
// uncapped (see the item-51 review, F3): a fence nested inside a list item
// is indented well past column 3 in the raw text, and rejecting it there
// left the fence invisible to the scanner, so a blank line inside it was
// wrongly treated as a safe flush point. The tradeoff is that a genuine
// 4-space-indented plain-text code block whose content happens to contain a
// backtick/tilde run can be misdetected as a fence — a rare, accepted,
// self-healing edge case (forceRenderDirtyBlocks re-renders the whole block
// from scratch once it finishes), consistent with 4-space code blocks being
// out of scope for this scanner.
// F16(b): indent is a raw character count (a tab counts as 1, not several
// visual columns), so the fenceIndent+3 closing-slack check is
// column-inconsistent for a tab-indented fence — direction depends on which
// side has the tab: on the OPENER it narrows tolerance (safe: a closer goes
// unrecognized, degrading to raw text like F15); on the CLOSER it can
// widen it (opener "```" + closer "\t\t```" counts as indent=2, closing a
// fence that visually (≈16 cols in a nested list) should still be open —
// the riskier premature-close/garbling direction). Left as-is: consistent
// with the rest of this character-based scanner, tabs in fence indentation
// are rare in LLM output, and a column-width-aware fix needs a tab-stop
// convention this code has no other reason to carry.
func fenceLineInfo(line string) (indent int, marker byte, run int, rest string, ok bool) {
	n := len(line)
	i := 0
	for i < n && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i >= n {
		return 0, 0, 0, "", false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return 0, 0, 0, "", false
	}
	j := i
	for j < n && line[j] == c {
		j++
	}
	run = j - i
	if run < 3 {
		return 0, 0, 0, "", false
	}
	return i, c, run, line[j:], true
}

// scan incrementally advances the fence/boundary scan over new content,
// updating inFence/fenceMarker/fenceLen/fenceIndent and safeUpto. It only
// looks at bytes after scanPos and only advances scanPos to just past the
// last '\n' in content — the same anchor streamWrap uses — so it never
// re-scans history and costs O(new bytes), not O(content).
func (s *mdStreamState) scan(content string) {
	nl := strings.LastIndexByte(content, '\n')
	if nl+1 <= s.scanPos {
		return
	}
	segment := content[s.scanPos : nl+1]
	lines := strings.Split(segment, "\n")
	lines = lines[:len(lines)-1] // trailing "" after the final '\n'
	pos := s.scanPos
	for _, line := range lines {
		lineEnd := pos + len(line) + 1 // +1 for the '\n'
		if s.inFence {
			// Closing check: any indent is accepted (F3) as long as it does
			// not run away arbitrarily far past the opening indent — bound
			// it to fenceIndent+3, the same "0-3 columns of slack" CommonMark
			// allows a closing fence relative to its container, so a closer
			// genuinely belonging to this fence is recognized while a
			// coincidental marker run deep inside unrelated, differently
			// indented prose is not.
			if indent, marker, run, rest, ok := fenceLineInfo(line); ok &&
				marker == s.fenceMarker && run >= s.fenceLen &&
				indent <= s.fenceIndent+3 &&
				strings.TrimSpace(rest) == "" {
				s.inFence = false
			}
		} else if indent, marker, run, _, ok := fenceLineInfo(line); ok {
			s.inFence = true
			s.fenceMarker = marker
			s.fenceLen = run
			s.fenceIndent = indent
		} else if strings.TrimSpace(line) == "" {
			// A blank line (or, tolerantly, a whitespace-only / bare-"\r"
			// line — F4) while no fence is open: safe to flush everything up
			// to and including this blank line's terminating '\n'.
			s.safeUpto = lineEnd
		}
		pos = lineEnd
	}
	s.scanPos = nl + 1
}

// renderStreamingMarkdown renders block idx's streaming text content as a
// stable, glamour-styled prefix plus a raw wrapped tail.
//
// The prefix grows only up to the latest safe "\n\n" boundary found by
// mdStreamState.scan (no fence open at that point); each newly safe segment
// is glamour-rendered exactly once and its lines are appended to
// renderedPrefixLines, which is never touched again. A blank line is
// appended after each segment's lines too (F2), preserving the paragraph
// separator that the safe boundary itself was found on — without it,
// consecutive flushed paragraphs read as jammed together until
// finalization pops the spacing back in. The tail — content[renderedUpto:]
// — is re-wrapped incrementally by a streamWrap the same way the whole
// block used to be, so a message with no safe boundary at all (one giant
// paragraph, one giant fence) produces byte-identical output to the
// pre-existing pure streamWrap path: renderedPrefixLines stays empty and
// the composed lines are exactly the tail's lines.
//
// Every byte reaches glamour at most once: segments partition the content,
// and a glamour call only fires on the rare delta that completes a
// paragraph (O(that paragraph)), never on the common per-token append.
//
// Per-token cost: rebuilding cachedLines (prefix lines + tail lines) every
// token is O(#lines), the same shape streamWrap's own copy-then-append
// already costs on the base path — not new. What WAS new, and wrong, is
// re-joining the entire prefix's bytes with strings.Join on every token: the
// prefix is glamour output (ANSI codes, lines padded to full width), so its
// byte count is much larger than the raw text it replaces, and doing that
// join once per streamed token turned a 10 KB reply into ~165 MB of garbage.
// renderedPrefixJoined caches that join, rebuilt only when a flush happens,
// so the per-token cost goes back to O(tail bytes), not O(prefix bytes).
func (m *TuiModel) renderStreamingMarkdown(idx int, content string) string {
	st := m.mdStreamState[idx]
	if st == nil {
		st = &mdStreamState{}
		m.mdStreamState[idx] = st
	}
	if st.renderedUpto > len(content) || st.scanPos > len(content) {
		// Content shrank — invariant broken (mirrors streamWrap.render's
		// identical guard at tui_render_block.go) — restart from scratch.
		*st = mdStreamState{}
	}
	st.scan(content)

	width := m.renderWidth()
	if st.safeUpto > st.renderedUpto {
		segment := content[st.renderedUpto:st.safeUpto]
		rendered := renderMarkdownWithCache(segment, false, width)
		if rendered != "" {
			newLines := strings.Split(rendered, "\n")
			newLines = append(newLines, "") // F2: keep the paragraph's blank separator
			st.renderedPrefixLines = append(st.renderedPrefixLines, newLines...)
			if st.renderedPrefixJoined == "" {
				st.renderedPrefixJoined = rendered + "\n"
			} else {
				st.renderedPrefixJoined = st.renderedPrefixJoined + "\n" + rendered + "\n"
			}
		}
		st.renderedUpto = st.safeUpto
		// The tail wrapper's offsets are relative to whatever string it's
		// fed; renderedUpto just moved, so the growing suffix it will be fed
		// changed underneath it — start a fresh one.
		m.streamWraps[idx] = &streamWrap{}
	}

	sw := m.streamWraps[idx]
	if sw == nil {
		sw = &streamWrap{}
		m.streamWraps[idx] = sw
	}
	// A tail of "" (everything flushed into the prefix, nothing pending)
	// contributes zero lines — not one blank line. streamWrap.render always
	// returns at least one ([""]) line for its input because content == ""
	// never used to reach it (tryRenderMarkdown short-circuits an empty
	// whole-block content before ever calling this function); here the
	// *tail* legitimately can be empty once a flush lands exactly at the
	// block's current end, and padding it with a spurious blank line would
	// make cachedLineCount overcount relative to what actually renders.
	var tailWrapped string
	var tailLines []string
	if tail := content[st.renderedUpto:]; tail != "" {
		// renderWidth, not m.width: with the sidebar open the only thing on
		// screen is the narrowed main column, so this cache must hold lines
		// wrapped at mainColumnWidth (see renderWidth). Caching full-width
		// lines here is what shredded markdown tables under the sidebar's
		// safety re-wrap.
		tailWrapped, tailLines = sw.render(tail, false, width)
	}

	lines := make([]string, 0, len(st.renderedPrefixLines)+len(tailLines))
	lines = append(lines, st.renderedPrefixLines...)
	lines = append(lines, tailLines...)
	// tailLines, when non-empty, never ends in a blank line — streamWrap.render
	// already trims that. So the only way `lines` can end in "" here is the F2
	// separator appended after the latest flushed paragraph, still waiting
	// with an empty tail for the next paragraph to start arriving. No cached
	// block anywhere in this codebase carries a trailing blank line as its
	// own content (buildAllFlatRenderLines trims exactly this at the whole-
	// transcript level, and totalRenderedLines()'s incremental math in
	// appendOrAppend assumes it too) — drop it here for the same reason, on
	// this transient composed view only; st.renderedPrefixLines itself keeps
	// the separator, since it still belongs once the tail is no longer empty.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	m.blocks[idx].cachedLineCount = len(lines)
	m.blocks[idx].cachedLines = lines

	// The string this function returns is only ever consulted by
	// getBlockLines for an `== ""` emptiness check (the real content lives
	// in m.blocks[idx].cachedLines, set above) — so it does not need to be
	// the exact byte-for-byte concatenation of prefix and tail, only
	// non-empty when there is anything to show. Rebuilding that exact
	// concatenation would copy the whole (possibly large) prefix on every
	// token, which is the exact cost F1 removes above; skip it.
	//
	// F16(a): this used to just `return tailWrapped`, trusting the unstated
	// invariant "len(tailLines) > 0 implies tailWrapped != ''". That's false:
	// streamWrap.render returns ("", [""]) for a tail that's currently just
	// pending whitespace/newlines, and the old "" then made getBlockLines
	// wipe the cachedLines just set above, vanishing an already-styled
	// block for a frame. Guard on len(lines) instead — the thing this
	// string's emptiness must actually track.
	if len(lines) == 0 {
		return ""
	}
	if tailWrapped != "" {
		return tailWrapped
	}
	return st.renderedPrefixJoined
}
