package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/stream"
)

// newTestPlainSink returns a plainSink and the buffers behind its stdout and
// stderr.
func newTestPlainSink() (*plainSink, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &plainSink{out: &out, err: &errOut}, &out, &errOut
}

func TestPlainSinkWritesTextToStdoutOnly(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.Text("hi")
	p.ToolCallStart("bash")
	p.End()

	if got := out.String(); got != "hi\n" {
		t.Errorf("stdout = %q, want %q", got, "hi\n")
	}
	if got := errOut.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
}

func TestPlainSinkTwoRoundsOneTrailingNewline(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.Request("first")
	p.Text("a")
	p.ToolCallStart("bash")
	p.ToolCallEnd("bash", "result")
	p.ToolFinish()
	p.Request("tool results")
	p.Text("b")
	p.End()
	p.End()

	if got := out.String(); got != "a\nb\n" {
		t.Errorf("stdout = %q, want %q", got, "a\nb\n")
	}
	if got := errOut.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
}

func TestPlainSinkNoTextWritesNothing(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.Request("prompt")
	p.Thinking("hmm")
	p.ToolCallStart("bash")
	p.End()

	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q, want both empty", out.String(), errOut.String())
	}
}

func TestPlainSinkErrorGoesToStderr(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.Error(errors.New("x"))

	if got := out.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
	if got := errOut.String(); !strings.Contains(got, "x") {
		t.Errorf("stderr = %q, want it to contain the error", got)
	}
}

func TestPlainSinkToolBlockGoesToStderr(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.ToolBlock("⏳ waiting for tools")
	p.ToolBlock("retry 1/5 — timeout")

	if got := out.String(); got != "" {
		t.Errorf("stdout = %q, want empty", got)
	}
	if got := errOut.String(); got != "retry 1/5 — timeout\n" {
		t.Errorf("stderr = %q, want only the retry notice", got)
	}
}

// plainSink must accept the usage events without writing anything.
func TestPlainSinkUsageIsSilent(t *testing.T) {
	p, out, errOut := newTestPlainSink()

	p.Summary(stream.Usage{}, stream.Stats{})
	p.Total(stream.Usage{})

	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("stdout = %q, stderr = %q, want both empty", out.String(), errOut.String())
	}
}
