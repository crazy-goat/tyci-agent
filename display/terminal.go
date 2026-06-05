package display

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/decodo/tyci-agent/stream"
)

const (
	clearLine = "\033[K"
	bgReset   = "\033[0m"
)

func TerminalIsDark() bool {
	cfb := os.Getenv("COLORFGBG")
	if cfb == "" {
		return true
	}
	parts := strings.Split(cfb, ";")
	if len(parts) < 2 {
		return true
	}
	bg, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return true
	}
	return bg < 8
}

type blockKind int

const (
	blockNone blockKind = iota
	blockThinking
	blockText
	blockTool
	blockUsage
)

type Terminal struct {
	bgTools    string
	bgThinking string
	bgUsage    string

	curBlock blockKind
	curBg    string

	termWidth int
	cursorCol int // current column position on the current terminal line
}

func NewTerminal() *Terminal {
	t := &Terminal{
		termWidth: getTerminalWidth(),
		cursorCol: 0,
	}
	if TerminalIsDark() {
		t.bgTools = "\033[48;2;18;18;42m"
		t.bgThinking = "\033[48;2;18;40;18m"
		t.bgUsage = "\033[48;2;70;70;70m"
	} else {
		t.bgTools = "\033[48;2;248;248;254m"
		t.bgThinking = "\033[48;2;248;253;248m"
		t.bgUsage = "\033[48;2;230;230;230m"
	}
	return t
}

func (t *Terminal) continueBlock(kind blockKind, bg string) bool {
	if t.curBlock == kind {
		return false
	}
	t.newBlock(kind, bg)
	return true
}

func (t *Terminal) newBlock(kind blockKind, bg string) {
	t.closeBlock()
	t.curBlock = kind
	t.curBg = bg
	t.cursorCol = 0
	if bg != "" {
		fmt.Fprint(os.Stdout, bg)
		fmt.Fprint(os.Stdout, clearLine) // wypełnij całą pierwszą linię tłem
	}
}

func (t *Terminal) closeBlock() {
	if t.curBlock == blockNone {
		return
	}
	if t.curBg != "" {
		fmt.Fprint(os.Stdout, clearLine) // wypełnij resztę linii tłem
		fmt.Fprint(os.Stdout, bgReset)
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout)
	t.curBlock = blockNone
	t.curBg = ""
	t.cursorCol = 0
}

func (t *Terminal) Thinking(text string) {
	isNew := t.continueBlock(blockThinking, t.bgThinking)
	var startCol int
	if isNew {
		startCol = visibleWidth("💭 ")
	} else {
		startCol = t.cursorCol
	}

	text = wrapText(text, t.termWidth, startCol)
	text = strings.ReplaceAll(text, "\n", "\n"+clearLine)

	if isNew {
		fmt.Fprint(os.Stdout, "💭 ")
	}
	fmt.Fprint(os.Stdout, text)

	// Update cursorCol based on what was printed
	if strings.HasSuffix(text, "\n") || strings.HasSuffix(text, clearLine+"\n") {
		t.cursorCol = 0
	} else {
		lastLine := text
		if idx := strings.LastIndex(text, "\n"); idx >= 0 {
			lastLine = text[idx+1:]
		}
		t.cursorCol = visibleWidth(lastLine)
		// When continuing a block without newlines, cursor stays on same line
		if !isNew && !strings.Contains(text, "\n") {
			t.cursorCol += startCol
		}
	}
	// If this is a new block and the output didn't contain newlines (single line),
	// the prompt offset only affects the first line. If there are multiple lines,
	// the last line is at column 0 and doesn't need the prompt offset.
	if isNew && t.cursorCol > 0 && !strings.Contains(text, "\n") {
		t.cursorCol += visibleWidth("💭 ")
	}
}

func (t *Terminal) Text(text string) {
	isNew := t.continueBlock(blockText, "")
	var startCol int
	if isNew {
		startCol = 0
	} else {
		startCol = t.cursorCol
	}

	// Text blocks don't have background, but we still need to wrap long lines
	// so they don't overflow the terminal. However, "normal" mode doesn't
	// wrap text; it's displayed as-is. The user expects text to flow naturally.
	// We'll just print directly without wrapping for text blocks.
	fmt.Fprint(os.Stdout, text)

	// Update cursorCol
	if strings.HasSuffix(text, "\n") {
		t.cursorCol = 0
	} else {
		// Find the last line's visible width
		lastLine := text
		if idx := strings.LastIndex(text, "\n"); idx >= 0 {
			lastLine = text[idx+1:]
		}
		t.cursorCol = visibleWidth(lastLine)
		// When continuing a block without newlines, cursor stays on same line
		if !isNew && !strings.Contains(text, "\n") {
			t.cursorCol += startCol
		}
	}
}

func (t *Terminal) ToolCallStart(name string) {
	t.newBlock(blockTool, t.bgTools)

	out := fmt.Sprintf("🔧 %s\n", name)
	out = wrapText(out, t.termWidth, 0)
	out = strings.ReplaceAll(out, "\n", "\n"+clearLine)
	fmt.Fprint(os.Stdout, out)
	t.cursorCol = 0
}

func (t *Terminal) ToolCallDelta(delta string) {
	if delta == "" {
		return
	}
	out := delta
	out = wrapText(out, t.termWidth, 0)
	out = strings.ReplaceAll(out, "\n", "\n"+clearLine)

	if t.curBg != "" {
		out = clearLine + out
	}

	fmt.Fprint(os.Stdout, out)

	if strings.HasSuffix(out, "\n") || strings.HasSuffix(out, clearLine+"\n") {
		t.cursorCol = 0
	} else {
		lastLine := out
		if idx := strings.LastIndex(out, "\n"); idx >= 0 {
			lastLine = out[idx+1:]
		}
		lastLine = strings.TrimPrefix(lastLine, clearLine)
		t.cursorCol = visibleWidth(lastLine)
	}
}

func (t *Terminal) ToolCallEnd(name string, result string) {
	if name == "read" || result == "" {
		return
	}
	// append result to the current tool block
	out := "\n" + result
	out = wrapText(out, t.termWidth, 0)
	out = strings.ReplaceAll(out, "\n", "\n"+clearLine)
	fmt.Fprint(os.Stdout, out)

	// Update cursorCol based on what was printed
	if strings.HasSuffix(out, "\n") || strings.HasSuffix(out, clearLine+"\n") {
		t.cursorCol = 0
	} else {
		lastLine := out
		if idx := strings.LastIndex(out, "\n"); idx >= 0 {
			lastLine = out[idx+1:]
		}
		t.cursorCol = visibleWidth(lastLine)
	}
}

func (t *Terminal) Summary(usage stream.Usage, stats stream.Stats) {
	t.newBlock(blockUsage, t.bgUsage)
	line := "Usage: " + buildUsageLine(usage, stats)
	line = wrapText(line, t.termWidth, 0)
	line = strings.ReplaceAll(line, "\n", "\n"+clearLine)
	fmt.Fprint(os.Stdout, line)

	// Update cursorCol
	if strings.HasSuffix(line, "\n") || strings.HasSuffix(line, clearLine+"\n") {
		t.cursorCol = 0
	} else {
		lastLine := line
		if idx := strings.LastIndex(line, "\n"); idx >= 0 {
			lastLine = line[idx+1:]
		}
		t.cursorCol = visibleWidth(lastLine)
	}
}

func (t *Terminal) Error(err error) {
	t.closeBlock()
	fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (t *Terminal) End() {
	t.closeBlock()
}

func parseArgs(arguments string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(arguments), &parsed); err != nil {
		return nil
	}
	return parsed
}

// wrapText splits long lines into multiple lines to fit within maxWidth.
// startCol is the number of columns already used on the first line (e.g., prompt).
// It preserves ANSI escape sequences and adds clearLine after each newline.
func wrapText(s string, maxWidth, startCol int) string {
	if maxWidth <= 0 {
		return s
	}

	// Tokenize: split into visible characters, escape sequences, and newlines.
	type token struct {
		isEscape bool // true for ANSI escape sequences
		value    string
	}
	var tokens []token
	var esc strings.Builder
	inEscape := false
	for _, r := range s {
		if inEscape {
			esc.WriteRune(r)
			if r == 'm' || r == 'K' || r == 'H' || r == 'J' {
				tokens = append(tokens, token{isEscape: true, value: esc.String()})
				esc.Reset()
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			esc.WriteRune(r)
			continue
		}
		if r == '\n' {
			tokens = append(tokens, token{isEscape: false, value: "\n"})
			continue
		}
		tokens = append(tokens, token{isEscape: false, value: string(r)})
	}
	if inEscape && esc.Len() > 0 {
		// Incomplete escape sequence at end - include it anyway
		tokens = append(tokens, token{isEscape: true, value: esc.String()})
		esc.Reset()
	}

	// Assemble output lines from tokens, wrapping at maxWidth visible chars.
	var result strings.Builder
	var lineTokens []token // tokens for the current output line
	visibleCount := startCol // account for the starting column offset

	flushLine := func() {
		for _, t := range lineTokens {
			result.WriteString(t.value)
		}
		lineTokens = nil
		visibleCount = 0
	}

	for _, tok := range tokens {
		if tok.isEscape {
			// Escape sequences stay with the current line
			lineTokens = append(lineTokens, tok)
			continue
		}
		if tok.value == "\n" {
			flushLine()
			result.WriteByte('\n')
			continue
		}
		// Visible character
		if visibleCount == maxWidth {
			flushLine()
			result.WriteString(clearLine)
			result.WriteByte('\n')
		}
		lineTokens = append(lineTokens, tok)
		visibleCount++
	}
	flushLine()
	return result.String()
}
