package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/display"
)

func TestParseRenameArgs_EmptyIsUsage(t *testing.T) {
	for _, arg := range []string{"", "   "} {
		_, err := parseRenameArgs(arg)
		if err == nil || err.Error() != "usage: /rename <title>" {
			t.Errorf("parseRenameArgs(%q) error = %v, want usage", arg, err)
		}
	}
}

func TestParseRenameArgs_TrimsAndCuts(t *testing.T) {
	got, err := parseRenameArgs("  " + strings.Repeat("ß", 100) + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(got)); n != 80 {
		t.Errorf("rune count = %d, want 80", n)
	}
}

type fakeRenameDisp struct {
	blocks []string
	errs   []error
	resets int
}

func (f *fakeRenameDisp) Error(err error)                           { f.errs = append(f.errs, err) }
func (f *fakeRenameDisp) ToolBlock(msg string)                      { f.blocks = append(f.blocks, msg) }
func (f *fakeRenameDisp) ResetStatus()                              { f.resets++ }
func (f *fakeRenameDisp) OpenResumePicker([]display.TuiResumeEntry) {}
func (f *fakeRenameDisp) OpenBtwList()                              {}

func TestHandleRenameCommand_UsageDoesNotRename(t *testing.T) {
	d := &fakeRenameDisp{}
	called := false
	ok := handleRenameCommand(d, "", func(string) (string, error) { called = true; return "", nil })
	if ok || called {
		t.Errorf("ok=%v called=%v, want no rename", ok, called)
	}
	if len(d.blocks) != 1 || d.blocks[0] != "usage: /rename <title>" {
		t.Errorf("blocks = %q, want usage line", d.blocks)
	}
	if d.resets != 1 {
		t.Errorf("ResetStatus calls = %d, want 1", d.resets)
	}
}

func TestHandleRenameCommand_RenamesAndReports(t *testing.T) {
	d := &fakeRenameDisp{}
	var got string
	ok := handleRenameCommand(d, " Fix login ", func(title string) (string, error) { got = title; return title, nil })
	if !ok || got != "Fix login" {
		t.Errorf("ok=%v got=%q", ok, got)
	}
	if len(d.blocks) != 1 || d.blocks[0] != "renamed: Fix login" {
		t.Errorf("blocks = %q", d.blocks)
	}
}

func TestHandleRenameCommand_RenameErrorIsShown(t *testing.T) {
	d := &fakeRenameDisp{}
	ok := handleRenameCommand(d, "x", func(string) (string, error) { return "", errors.New("disk full") })
	if ok || len(d.errs) != 1 {
		t.Errorf("ok=%v errs=%v, want one error", ok, d.errs)
	}
}
