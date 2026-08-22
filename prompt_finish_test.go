package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/stream"
)

// This file is finding (2) from item 8 batch 2's review: os.Exit inside
// finishPromptRun (the `tyci run` / cron path, since cron just shells out
// to `tyci run`) used to bypass every deferred cleanup in runCmd, so an
// errored or Ctrl-C'd run never ran tools.ShutdownMCP() and leaked a
// connected MCP server's process. finishPromptRun now takes a cleanup func
// and calls it right before every exit; exitFunc (prompt_finish.go) is the
// os.Exit indirection that lets these tests observe that without actually
// terminating the test binary.

// noopDisplay is a display.Display that does nothing, for driving
// finishPromptRun without a real terminal/minimal renderer.
type noopDisplay struct{}

func (noopDisplay) Request(content string)                         {}
func (noopDisplay) Thinking(text string)                           {}
func (noopDisplay) Text(text string)                               {}
func (noopDisplay) ToolCallStart(name string)                      {}
func (noopDisplay) ToolCallDelta(delta string)                     {}
func (noopDisplay) ToolCallEnd(name string, result string)         {}
func (noopDisplay) ToolFinish()                                    {}
func (noopDisplay) ToolBlock(msg string)                           {}
func (noopDisplay) Summary(usage stream.Usage, stats stream.Stats) {}
func (noopDisplay) Total(usage stream.Usage)                       {}
func (noopDisplay) Error(err error)                                {}
func (noopDisplay) End()                                           {}

// newFinishTestConductor builds a *conductor.Conductor with no session (so
// EndSession/SessionPath are no-ops) and a fake model client -- enough to
// drive finishPromptRun without touching disk or a real provider.
func newFinishTestConductor() *conductor.Conductor {
	return conductor.New(conductor.Options{
		Client: &connectortest.Fake{ProviderName: "finish-test-prov", ModelName: "m1"},
		Sink:   noopDisplay{},
		Config: agent.Config{},
	})
}

// withCapturedExit overrides exitFunc for the duration of the test and
// returns accessors for whether it was called and with what code.
func withCapturedExit(t *testing.T) (called func() bool, code func() int) {
	t.Helper()
	orig := exitFunc
	var wasCalled bool
	var exitCode int
	exitFunc = func(c int) {
		wasCalled = true
		exitCode = c
	}
	t.Cleanup(func() { exitFunc = orig })
	return func() bool { return wasCalled }, func() int { return exitCode }
}

// TestFinishPromptRun_ErrorPath_CallsCleanupBeforeExit is the test for
// finding (2): it fails if the cleanup hook is dropped from the error
// path, or if it runs after exitFunc instead of before (which, with the
// real os.Exit, would mean it never runs at all).
func TestFinishPromptRun_ErrorPath_CallsCleanupBeforeExit(t *testing.T) {
	exited, code := withCapturedExit(t)

	var cleanupCalled bool
	cond := newFinishTestConductor()
	finishPromptRun(cond, noopDisplay{}, fmt.Errorf("boom"), func() { cleanupCalled = true })

	if !exited() {
		t.Fatalf("expected exitFunc to be called on the error path")
	}
	if code() != 1 {
		t.Fatalf("expected exit code 1, got %d", code())
	}
	if !cleanupCalled {
		t.Fatalf("expected cleanup to run on the error path before exit -- this is exactly what os.Exit used to skip")
	}
}

// TestFinishPromptRun_CanceledPath_CallsCleanupBeforeExit covers the
// Ctrl-C path (context.Canceled), which exits 130 through a separate
// branch in finishPromptRun than the generic error path above.
func TestFinishPromptRun_CanceledPath_CallsCleanupBeforeExit(t *testing.T) {
	exited, code := withCapturedExit(t)

	var cleanupCalled bool
	cond := newFinishTestConductor()
	finishPromptRun(cond, noopDisplay{}, context.Canceled, func() { cleanupCalled = true })

	if !exited() {
		t.Fatalf("expected exitFunc to be called on the canceled path")
	}
	if code() != 130 {
		t.Fatalf("expected exit code 130, got %d", code())
	}
	if !cleanupCalled {
		t.Fatalf("expected cleanup to run on the canceled (Ctrl-C) path before exit")
	}
}

// TestFinishPromptRun_NoErrorPath_ReturnsWithoutExitOrCleanup checks the
// other half of the contract: the successful fallthrough must NOT call
// exitFunc or the cleanup hook itself -- that path returns normally, and
// runCmd's own `defer cleanup()` (commands.go) is what covers it. If
// finishPromptRun called cleanup here too, a normal `tyci run` would run
// tools.ShutdownMCP() (and close the debug log) twice.
func TestFinishPromptRun_NoErrorPath_ReturnsWithoutExitOrCleanup(t *testing.T) {
	exited, _ := withCapturedExit(t)

	var cleanupCalled bool
	cond := newFinishTestConductor()
	finishPromptRun(cond, noopDisplay{}, nil, func() { cleanupCalled = true })

	if exited() {
		t.Fatalf("did not expect exitFunc to be called on the no-error path")
	}
	if cleanupCalled {
		t.Fatalf("did not expect finishPromptRun to call cleanup itself on the no-error path; the caller's own defer covers it")
	}
}
