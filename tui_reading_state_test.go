package main

// Item 37: bare /resume and /btw (and /resume --all, /msg, /compact) at an
// idle TUI prompt could leave display.TuiModel.reading stuck false forever
// — display.TuiModel.submit() sets it false the instant any of these lines
// is submitted (handleLocalSlashCommand refuses to handle ANY command
// locally while idle, so they all fall through to submit()), and none of
// these six branches in runTUI ever ran a real agent turn, which is the
// only other thing that restores it (a "done"/"reset" tuiMsgBlock).
//
// runTUI itself drives a real *display.TUI over a live bubbletea Program
// and isn't practically unit-testable in this codebase. These tests
// instead pin the actual fix: handleBareResumeCommand/handleResumeAllCommand/
// handleBareBtwCommand/handleBtwQuestionCommand/handleMsgSlashCommand/
// handleCompactCommand (tui_mode.go) — the functions runTUI's switch now
// delegates to — via a fake slashCommandDisplay that records exactly what
// was called. Each test asserts ResetStatus fires on EVERY exit path
// (error, empty result, success), which is the actual regression: revert
// any of these functions to skip ResetStatus on one branch and the
// corresponding test fails.

import (
	"errors"
	"testing"

	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/session"
)

// fakeSlashDisplay implements slashCommandDisplay and records every call, in
// order, so a test can assert both "ResetStatus fired" and "it fired before
// the picker/error/toolblock it's guarding".
type fakeSlashDisplay struct {
	calls              []string
	resumePickerCalled bool
	btwListCalled      bool
	lastError          error
	lastToolBlock      string
}

func (f *fakeSlashDisplay) Error(err error) {
	f.calls = append(f.calls, "Error")
	f.lastError = err
}
func (f *fakeSlashDisplay) ToolBlock(msg string) {
	f.calls = append(f.calls, "ToolBlock")
	f.lastToolBlock = msg
}
func (f *fakeSlashDisplay) ResetStatus() {
	f.calls = append(f.calls, "ResetStatus")
}
func (f *fakeSlashDisplay) OpenResumePicker(entries []display.TuiResumeEntry) {
	f.calls = append(f.calls, "OpenResumePicker")
	f.resumePickerCalled = true
}
func (f *fakeSlashDisplay) OpenBtwList() {
	f.calls = append(f.calls, "OpenBtwList")
	f.btwListCalled = true
}

// resetStatusCalled reports whether ResetStatus is anywhere in the call
// log — the one thing every one of these handlers must guarantee.
func (f *fakeSlashDisplay) resetStatusCalled() bool {
	for _, c := range f.calls {
		if c == "ResetStatus" {
			return true
		}
	}
	return false
}

func TestHandleBareResumeCommand_RestoresReadingOnError(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleBareResumeCommand(f, "/some/dir", func(string) ([]session.ResumeEntry, error) {
		return nil, errors.New("boom")
	})
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called even when listing sessions fails")
	}
	if f.lastError == nil {
		t.Error("expected the error to be surfaced")
	}
	if f.resumePickerCalled {
		t.Error("picker must not open after an error")
	}
}

func TestHandleBareResumeCommand_RestoresReadingOnEmptyList(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleBareResumeCommand(f, "/some/dir", func(string) ([]session.ResumeEntry, error) {
		return nil, nil
	})
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called even when there are no sessions to list")
	}
	if f.resumePickerCalled {
		t.Error("picker must not open with nothing to show")
	}
}

func TestHandleBareResumeCommand_RestoresReadingOnSuccess(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleBareResumeCommand(f, "/some/dir", func(string) ([]session.ResumeEntry, error) {
		return []session.ResumeEntry{{Path: "/some/dir/a.jsonl"}}, nil
	})
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called even on a successful open")
	}
	if !f.resumePickerCalled {
		t.Error("expected the picker to actually open with sessions present")
	}
	if f.calls[0] != "ResetStatus" {
		t.Errorf("ResetStatus must run before anything else, got call order %v", f.calls)
	}
}

func TestHandleResumeAllCommand_RestoresReadingOnError(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleResumeAllCommand(f, func() ([]session.ResumeEntry, error) {
		return nil, errors.New("boom")
	})
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called even when listing sessions fails")
	}
}

func TestHandleResumeAllCommand_RestoresReadingOnEmptyList(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleResumeAllCommand(f, func() ([]session.ResumeEntry, error) { return nil, nil })
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called even when there are no sessions recorded")
	}
}

func TestHandleResumeAllCommand_RestoresReadingOnSuccess(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleResumeAllCommand(f, func() ([]session.ResumeEntry, error) {
		return []session.ResumeEntry{{Path: "/x/a.jsonl"}}, nil
	})
	if !f.resetStatusCalled() || !f.resumePickerCalled {
		t.Fatalf("expected both ResetStatus and the picker to fire, got calls %v", f.calls)
	}
}

func TestHandleBareBtwCommand_RestoresReadingBeforeOpeningTheList(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleBareBtwCommand(f)
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called — opening the list never runs the agent")
	}
	if !f.btwListCalled {
		t.Error("expected the btw list to actually open")
	}
	if f.calls[0] != "ResetStatus" || f.calls[1] != "OpenBtwList" {
		t.Errorf("expected ResetStatus before OpenBtwList, got %v", f.calls)
	}
}

func TestHandleBtwQuestionCommand_RestoresReadingOnEmptyQuestion(t *testing.T) {
	f := &fakeSlashDisplay{}
	spawned := false
	handleBtwQuestionCommand(f, "   ", func(string) { spawned = true })
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called when the question is empty")
	}
	if spawned {
		t.Error("must not spawn a side conversation with no question")
	}
}

func TestHandleBtwQuestionCommand_RestoresReadingOnSpawn(t *testing.T) {
	f := &fakeSlashDisplay{}
	var gotQuestion string
	handleBtwQuestionCommand(f, " why is this slow? ", func(q string) { gotQuestion = q })
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called — the side conversation never restores it on its own")
	}
	if gotQuestion != "why is this slow?" {
		t.Errorf("expected the trimmed question to reach spawn, got %q", gotQuestion)
	}
}

func TestHandleMsgSlashCommand_RestoresReading(t *testing.T) {
	f := &fakeSlashDisplay{}
	var posted string
	handleMsgSlashCommand(f, "job-1 hello", func(arg string) { posted = arg })
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called — /msg never runs the agent")
	}
	if posted != "job-1 hello" {
		t.Errorf("expected the arg to reach post, got %q", posted)
	}
}

func TestHandleCompactCommand_RestoresReadingOnError(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleCompactCommand(f, func() (string, bool) { return "nothing to compact yet", true })
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called on a /compact error")
	}
	if f.lastError == nil || f.lastError.Error() != "nothing to compact yet" {
		t.Errorf("expected the error surfaced, got %v", f.lastError)
	}
}

func TestHandleCompactCommand_RestoresReadingOnSuccess(t *testing.T) {
	f := &fakeSlashDisplay{}
	handleCompactCommand(f, func() (string, bool) {
		return "History compacted; raw record: /tmp/x.md", false
	})
	if !f.resetStatusCalled() {
		t.Fatal("ResetStatus must be called on a /compact success")
	}
	if f.lastToolBlock == "" {
		t.Error("expected the success message to be surfaced")
	}
	if f.calls[0] != "ResetStatus" {
		t.Errorf("ResetStatus must run before the compact work's result is rendered, got %v", f.calls)
	}
}

// TestFakeSlashDisplaySatisfiesInterface is a compile-time-adjacent guard:
// if slashCommandDisplay's method set ever grows, every test above still
// compiles against the interface, not a concrete *display.TUI, so a
// forgotten fake method fails loudly here instead of masking a gap.
func TestFakeSlashDisplaySatisfiesInterface(t *testing.T) {
	var _ slashCommandDisplay = &fakeSlashDisplay{}
	var _ slashCommandDisplay = (*display.TUI)(nil)
}
