package main

import (
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/internal/rolememory"
	"github.com/crazy-goat/tyci-agent/tools"
)

// The role note of an agent reaches the wire system prompt of that role, and
// of no other role.
func TestRunTaskWithSystem_RoleNoteIsInThatRolesPrompt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := rolememory.Write(home, "tyci-agent", "worker", "NOTE-FOR-WORKER"); err != nil {
		t.Fatal(err)
	}
	opts := tools.SubagentOptions{SystemPromptMode: "append"}
	r := &agentRunner{}

	worker := connectortest.Text("ok")
	ctx := tools.WithRoleContext(connector.WithModelClient(context.Background(), worker), "tyci-agent", "worker")
	if _, err := r.RunTaskWithSystem(ctx, "task", "", "worker body", opts); err != nil {
		t.Fatal(err)
	}
	sys := worker.Requests()[0].System
	if !strings.Contains(sys, "NOTE-FOR-WORKER") {
		t.Fatalf("the role note is missing from the system prompt:\n%s", sys)
	}
	if !strings.Contains(sys, "worker body") {
		t.Errorf("the role body is missing from the system prompt")
	}

	review := connectortest.Text("ok")
	ctx = tools.WithRoleContext(connector.WithModelClient(context.Background(), review), "tyci-agent", "review")
	if _, err := r.RunTaskWithSystem(ctx, "task", "", "review body", opts); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(review.Requests()[0].System, "NOTE-FOR-WORKER") {
		t.Fatal("the review role must not see the worker note")
	}
}

// Without a role in the context there is no role note, and a role without a
// note adds nothing.
func TestRoleMemorySection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := roleMemorySection(context.Background()); got != "" {
		t.Fatalf("no role: got %q", got)
	}
	ctx := tools.WithRoleContext(context.Background(), "tyci-agent", "worker")
	if got := roleMemorySection(ctx); got != "" {
		t.Fatalf("no note yet: got %q", got)
	}
}
