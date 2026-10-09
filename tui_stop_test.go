package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseStopArgs(t *testing.T) {
	if _, _, err := parseStopArgs(""); err == nil || !strings.HasPrefix(err.Error(), "usage: /stop") {
		t.Fatalf("bare /stop: err = %v, want the usage line", err)
	}
	run, reason, err := parseStopArgs(" abc user text here ")
	if err != nil || run != "abc" || reason != "user text here" {
		t.Fatalf("run %q reason %q err %v", run, reason, err)
	}
	run, reason, err = parseStopArgs("abc")
	if err != nil || run != "abc" || reason != "" {
		t.Fatalf("run only: run %q reason %q err %v", run, reason, err)
	}
}

// Stopping a run never runs the agent, so the status must be restored on every path.
func TestHandleStopCommand_RestoresReading(t *testing.T) {
	cases := map[string]func(run, reason string) (any, error){
		"success": func(run, _ string) (any, error) { return map[string]any{"run": run, "status": "stopped"}, nil },
		"error":   func(string, string) (any, error) { return nil, errors.New("not active") },
		"usage":   nil,
	}
	for name, stop := range cases {
		f := &fakeSlashDisplay{}
		arg := " r1"
		if name == "usage" {
			arg = ""
		}
		handleStopCommand(f, arg, stop)
		if !f.resetStatusCalled() {
			t.Errorf("%s: ResetStatus not called: %v", name, f.calls)
		}
		if f.calls[0] != "ResetStatus" {
			t.Errorf("%s: ResetStatus must come first: %v", name, f.calls)
		}
	}
}

func TestStopCommandOutput(t *testing.T) {
	f := &fakeSlashDisplay{}
	stopCommandOutput(f, "r1 too slow", func(run, reason string) (any, error) {
		if run != "r1" || reason != "too slow" {
			t.Fatalf("run %q reason %q", run, reason)
		}
		return map[string]any{"status": "stopped"}, nil
	})
	if f.lastToolBlock != `{"status":"stopped"}` {
		t.Fatalf("tool block = %q", f.lastToolBlock)
	}
	if f.resetStatusCalled() {
		t.Fatal("the mid-turn path must not reset the status")
	}
	f = &fakeSlashDisplay{}
	stopCommandOutput(f, "", nil)
	if f.lastError == nil || !strings.Contains(f.lastError.Error(), "usage: /stop <run> [reason]") {
		t.Fatalf("usage error = %v", f.lastError)
	}
}
