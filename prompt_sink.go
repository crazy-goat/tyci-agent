package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/stream"
)

// pendingToolMarker is the ToolBlock message runOnce sends while it waits
// for tool calls. It is progress noise for a plain run, so plainSink skips it.
const pendingToolMarker = "⏳"

var _ display.Display = (*plainSink)(nil)

// plainSink implements display.Display for `tyci run`. Assistant text goes
// to out. Notices (retries, fallbacks, warnings) and errors go to errOut.
// Tool calls, thinking and usage are not shown, so a pipe or a scheduled
// job gets only the answer.
type plainSink struct {
	out, err io.Writer

	// lineOpen is true when the last text written to out does not end with
	// a newline.
	lineOpen bool
}

// Request starts a new round. A round that ended without a newline gets one,
// so the text of two rounds does not run together.
func (p *plainSink) Request(string) { p.closeLine() }

func (p *plainSink) Text(s string) {
	if s == "" {
		return
	}
	_, _ = io.WriteString(p.out, s)
	p.lineOpen = !strings.HasSuffix(s, "\n")
}

// ToolBlock writes a notice to stderr. The pending-tools marker is skipped.
func (p *plainSink) ToolBlock(msg string) {
	if msg == "" || strings.HasPrefix(msg, pendingToolMarker) {
		return
	}
	p.closeLine()
	_, _ = fmt.Fprintln(p.err, msg)
}

func (p *plainSink) Error(err error) {
	p.closeLine()
	_, _ = fmt.Fprintf(p.err, "Error: %v\n", err)
}

// End closes the output with exactly one newline when text was written.
// Repeated calls write nothing more.
func (p *plainSink) End() { p.closeLine() }

func (p *plainSink) closeLine() {
	if p.lineOpen {
		_, _ = io.WriteString(p.out, "\n")
		p.lineOpen = false
	}
}

func (p *plainSink) Thinking(string)                    {}
func (p *plainSink) ToolCallStart(string)               {}
func (p *plainSink) ToolCallDelta(string)               {}
func (p *plainSink) ToolCallEnd(string, string)         {}
func (p *plainSink) ToolFinish()                        {}
func (p *plainSink) Summary(stream.Usage, stream.Stats) {}
func (p *plainSink) Total(stream.Usage)                 {}
