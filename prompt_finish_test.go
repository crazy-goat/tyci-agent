package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/stream"
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

func TestFinishPromptRun_ErrorPath_ReturnsOne(t *testing.T) {
	if code := finishPromptRun(newFinishTestConductor(), noopDisplay{}, fmt.Errorf("boom")); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestFinishPromptRun_CanceledPath_Returns130(t *testing.T) {
	if code := finishPromptRun(newFinishTestConductor(), noopDisplay{}, context.Canceled); code != 130 {
		t.Fatalf("exit code = %d, want 130", code)
	}
}

func TestFinishPromptRun_NoErrorPath_Returns0(t *testing.T) {
	if code := finishPromptRun(newFinishTestConductor(), noopDisplay{}, nil); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}
