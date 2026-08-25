package main

// Regression tests for TODO item 37: a slash command typed at an IDLE TUI
// prompt goes through submit(), which flips the model to busy (reading=false)
// and hands the line to runTUI. Every successful command path in runTUI must
// post something that flips the model back to idle — ResetStatus (a "done"
// block) or Reset — or the input stays stuck in the busy state: the next
// Enter lands in the pending-message queue instead of submitting, and the
// status ticker spins forever.
//
// These tests drive the REAL runTUI loop with a real (headless) TUI: the
// terminal's stdin/stdout are swapped for pipes, keystrokes are written in,
// and the idle state is proven by typing a sentinel prompt afterwards — an
// idle model submits it to the model (the fake client sees it in a request);
// a stuck-busy model queues it instead (HasPendingMessages goes true and the
// fake never sees it). Each test fails when the matching ResetStatus call in
// runTUI is reverted.
//
// Harness rules shared with wiring_test.go / btw_test.go: package-level
// globals (JobRegistry, JobNotices) are swapped, os.Stdin/os.Stdout are
// replaced — so no t.Parallel anywhere in this file.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/jobs"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// tuiTestRig is a running runTUI loop backed by a headless TUI whose stdin
// is a pipe the test writes keystrokes into.
type tuiTestRig struct {
	t    *testing.T
	disp *display.TUI
	fake *connectortest.Fake
	in   *os.File // write keystrokes here
	done chan struct{}
}

// startTUITestRig boots the real runTUI loop on a fake model client. HOME is
// redirected to a temp dir so session listing (/resume) is deterministic, and
// the working directory is a fresh temp dir so the project key is unique to
// this test.
func startTUITestRig(t *testing.T) *tuiTestRig {
	t.Helper()
	return startTUITestRigWithFake(t, connectortest.Text("ok"))
}

// startTUITestRigWithFake is startTUITestRig with a caller-supplied model
// client, for tests that need per-request behavior (e.g. a /btw fork that
// stays in flight long enough to type behind it).
func startTUITestRigWithFake(t *testing.T, fake *connectortest.Fake) *tuiTestRig {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	prevRegistry, prevNotices := JobRegistry, JobNotices
	JobRegistry, JobNotices = jobs.NewRegistry(), jobs.NewNotifier()
	t.Cleanup(func() { JobRegistry, JobNotices = prevRegistry, prevNotices })

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prevIn, prevOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = prevIn, prevOut
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
	})
	// The painter writes frames to stdout; drain the pipe so a full buffer
	// can never block the event loop.
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := outR.Read(buf); err != nil {
				return
			}
		}
	}()

	disp := display.NewTUI("fake/fake-1", "", []string{"fake/fake-1"}, nil, nil, nil, "", nil, 0, 0, 0, false)
	cond := conductor.New(conductor.Options{
		Client: fake,
		Sink:   disp,
		Config: agent.Config{MaxRetries: 1},
	})

	rig := &tuiTestRig{t: t, disp: disp, fake: fake, in: inW, done: make(chan struct{})}
	go func() {
		defer close(rig.done)
		runTUI(cond, disp, context.Background())
	}()
	t.Cleanup(func() {
		disp.Close()
		select {
		case <-rig.done:
		case <-time.After(5 * time.Second):
			t.Error("runTUI did not shut down after TUI close")
		}
	})
	// Give the bubbletea program a moment to start reading the pipe.
	time.Sleep(300 * time.Millisecond)
	return rig
}

// typeLine writes text plus Enter, as a person at the keyboard would.
func (r *tuiTestRig) typeLine(text string) {
	r.t.Helper()
	if _, err := r.in.WriteString(text + "\r"); err != nil {
		r.t.Fatalf("write keystrokes: %v", err)
	}
}

// pressEsc sends the closest thing to a lone Esc keypress that survives the
// harness. Two layers conspire against a plain "\x1b" on a pipe stdin:
//
//  1. display's sanitizeReader holds back a trailing 0x1b as a potential
//     split SGR-mouse prefix, so a lone Esc merges with the NEXT write
//     into "\x1b" + that write's first byte (an Alt-modified key) — the
//     Esc never lands and the following prompt loses its first character.
//  2. bubbletea's parser (key.go detectOneMsg) never reports a trailing
//     0x1b as KeyEscape while more data can arrive — always true on a pipe.
//
// The recipe "\x1b\x1b\x1b\x00": the filter passes all four bytes through
// (no trailing 0x1b), and bubbletea's sequence table maps "\x1b\x1b" to
// Key{Type: KeyEscape, Alt: true} followed by a harmless Alt+NUL. Every
// popup handler matches on msg.Type == tea.KeyEscape regardless of Alt, so
// this behaves exactly like Esc. (On a real terminal none of this applies:
// the Esc arrives as its own read and the short-read path flushes it.)
func (r *tuiTestRig) pressEsc() {
	r.t.Helper()
	if _, err := r.in.WriteString("\x1b\x1b\x1b\x00"); err != nil {
		r.t.Fatalf("write ESC: %v", err)
	}
}

// sawSentinel reports whether the fake model client has received a request
// containing the sentinel text, i.e. whether the TUI was idle enough to
// SUBMIT the sentinel line as a prompt.
func (r *tuiTestRig) sawSentinel(sentinel string) bool {
	for _, req := range r.fake.Requests() {
		for _, msg := range req.Messages {
			for _, block := range msg.Content {
				if strings.Contains(block.Text, sentinel) {
					return true
				}
			}
		}
	}
	return false
}

// assertIdleAfter is the regression assertion: after the command under test
// has been processed, the model must be back in the idle/read state. Proven
// by typing a unique sentinel prompt: idle → submitted to the model (the
// fake sees it, HasPendingMessages stays false); stuck busy → queued, the
// fake never sees it, and this fails.
func (r *tuiTestRig) assertIdleAfter(commandDesc, sentinel string) {
	r.t.Helper()
	r.typeLine(sentinel)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.sawSentinel(sentinel) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	if r.disp.HasPendingMessages() {
		r.t.Fatalf("%s left the TUI busy: the follow-up prompt %q was queued instead of submitted", commandDesc, sentinel)
	}
	r.t.Fatalf("%s: follow-up prompt %q never reached the model", commandDesc, sentinel)
}

// writeSessionFile records a minimal resumable session for the current temp
// project, so bare "/resume" finds an entry and opens the picker.
func writeSessionFile(t *testing.T) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := session.SessionDir(wd)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	header, err := json.Marshal(map[string]any{
		"type":      "session",
		"version":   1,
		"id":        "rig-sess",
		"timestamp": "2025-01-01T00:00:00Z",
		"cwd":       wd,
		"model":     "fake-1",
		"provider":  "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := `{"type":"message","id":"m1","timestamp":"2025-01-01T00:00:01Z","message":{"role":"user","content":[{"type":"text","text":"earlier prompt"}]}}`
	path := filepath.Join(dir, "20250101T000000Z_rigsess.jsonl")
	if err := os.WriteFile(path, []byte(string(header)+"\n"+msg+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBareBtwRestoresIdle: bare "/btw" opens the side-conversation list. The
// popup posts nothing that touches the reading flag, so runTUI must
// ResetStatus itself. (The Esc closes the popup first, the way a person
// would — while it is open it legitimately owns the keyboard.)
func TestBareBtwRestoresIdle(t *testing.T) {
	rig := startTUITestRig(t)
	rig.typeLine("/btw")
	time.Sleep(500 * time.Millisecond) // let runTUI service the command
	rig.pressEsc()
	time.Sleep(200 * time.Millisecond)
	rig.assertIdleAfter(`bare "/btw"`, "ping-after-bare-btw")
}

// TestBtwQuestionRestoresIdle: "/btw <question>" forks a background side
// conversation and opens its live modal. The fork streams to its own entry;
// nothing it posts touches the main model's reading flag.
//
// The fork's model call is deliberately slow: the fork's completion notice
// would otherwise start a turn whose Done() restores the reading flag as a
// side effect, masking the bug (the person typing behind a still-running
// fork is the real-world case). The sentinel is typed while the fork is
// guaranteed to be in flight.
func TestBtwQuestionRestoresIdle(t *testing.T) {
	const question = "why is the sky blue?"
	slowFork := &connectortest.Fake{
		Script: func(_ int, req connector.Request) []stream.Event {
			for _, msg := range req.Messages {
				for _, b := range msg.Content {
					if strings.Contains(b.Text, question) {
						// The fork's own request: answer slowly enough that
						// the test types behind it. Main-thread turns (the
						// sentinel prompt, the completion notice) stay fast.
						time.Sleep(3 * time.Second)
						return []stream.Event{stream.TextDelta{Text: "ok"}, stream.Finish{Reason: "stop"}}
					}
				}
			}
			return []stream.Event{stream.TextDelta{Text: "ok"}, stream.Finish{Reason: "stop"}}
		},
	}
	rig := startTUITestRigWithFake(t, slowFork)
	rig.typeLine("/btw " + question)
	time.Sleep(500 * time.Millisecond) // fork starts (in flight ~3s), modal opens
	rig.pressEsc()                     // close the modal; the fork keeps running
	time.Sleep(200 * time.Millisecond)
	rig.assertIdleAfter(`"/btw <question>"`, "ping-after-btw-question")
}

// TestBareResumeNoSessionsRestoresIdle: bare "/resume" with no recorded
// sessions shows an info block and must restore idle — no picker opens, so
// no later message will do it.
func TestBareResumeNoSessionsRestoresIdle(t *testing.T) {
	rig := startTUITestRig(t)
	rig.typeLine("/resume")
	time.Sleep(500 * time.Millisecond)
	rig.assertIdleAfter(`bare "/resume" (no sessions)`, "ping-after-bare-resume")
}

// TestResumePickerEscRestoresIdle: bare "/resume" with a recorded session
// opens the picker; pressing Esc must return the model to idle. This is the
// SelectedResume()=="" branch, which previously just continued.
func TestResumePickerEscRestoresIdle(t *testing.T) {
	writeSessionFile(t)
	rig := startTUITestRig(t)
	rig.typeLine("/resume")
	time.Sleep(500 * time.Millisecond) // picker opens
	rig.pressEsc()
	time.Sleep(500 * time.Millisecond) // runTUI receives ""
	rig.assertIdleAfter(`"/resume" picker Esc`, "ping-after-resume-esc")
}

// TestCompactRestoresIdle: bare "/compact" fails (empty summary) in this
// rig, but even the error path went through submit()'s busy flip, so idle
// must be restored. (Before the fix, neither the success nor the error
// branch of /compact posted anything.)
func TestCompactRestoresIdle(t *testing.T) {
	rig := startTUITestRig(t)
	rig.typeLine("/compact")
	time.Sleep(500 * time.Millisecond)
	rig.assertIdleAfter(`"/compact"`, "ping-after-compact")
}

// TestMsgRestoresIdle: "/msg <job> <text>" to a nonexistent job surfaces an
// error block — which posts no "done" — so the command path must restore
// idle itself.
func TestMsgRestoresIdle(t *testing.T) {
	rig := startTUITestRig(t)
	rig.typeLine("/msg #99 hello?")
	time.Sleep(500 * time.Millisecond)
	rig.assertIdleAfter(`"/msg"`, "ping-after-msg")
}

// TestResumeAllNoSessionsRestoresIdle: "/resume --all" with an empty HOME
// finds nothing and must restore idle after the info block.
func TestResumeAllNoSessionsRestoresIdle(t *testing.T) {
	rig := startTUITestRig(t)
	rig.typeLine("/resume --all")
	time.Sleep(500 * time.Millisecond)
	rig.assertIdleAfter(`"/resume --all"`, "ping-after-resume-all")
}

// TestNormalPromptStillWorks is the control: an ordinary prompt at an idle
// prompt reaches the model without any of the fixes above being involved,
// proving the harness itself is sound.
func TestNormalPromptStillWorks(t *testing.T) {
	rig := startTUITestRig(t)
	rig.assertIdleAfter("harness control", fmt.Sprintf("plain-prompt-%d", time.Now().UnixNano()))
}
