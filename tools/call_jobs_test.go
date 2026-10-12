package tools

import (
	"context"
	"testing"
)

// TestCallIDAliasResolvesJob: a backgrounded call shows its call id (cN) to
// the model. wait and kill_job must accept that id, and only for the agent
// that made the call.
func TestCallIDAliasResolvesJob(t *testing.T) {
	ctx := context.WithValue(context.Background(), CallIDCtxKey{}, "c7")
	BindCallJob(ctx, "job-42")

	if got := resolveCallID("", "c7"); got != "job-42" {
		t.Fatalf("resolveCallID(main, c7) = %q, want job-42", got)
	}
	if got := resolveCallID("sub-1", "c7"); got != "c7" {
		t.Fatalf("resolveCallID(other owner, c7) = %q, want c7 unchanged", got)
	}
	if got := resolveCallID("", "job-42"); got != "job-42" {
		t.Fatalf("a job id must pass through, got %q", got)
	}

	// A job started without a call id binds nothing.
	BindCallJob(context.Background(), "job-43")
	if got := resolveCallID("", "job-43"); got != "job-43" {
		t.Fatalf("unbound id = %q, want unchanged", got)
	}
}

// TestCallIDAliasInsideSubagent: a child agent binds its calls under its own
// job id, so the same cN in two agents names two different jobs.
func TestCallIDAliasInsideSubagent(t *testing.T) {
	child := context.WithValue(context.Background(), JobIDCtxKey{}, "sub-9")
	child = context.WithValue(child, CallIDCtxKey{}, "c1")
	BindCallJob(child, "job-child")

	main := context.WithValue(context.Background(), CallIDCtxKey{}, "c1")
	BindCallJob(main, "job-main")

	if got := resolveCallID("sub-9", "c1"); got != "job-child" {
		t.Fatalf("child resolve = %q, want job-child", got)
	}
	if got := resolveCallID("", "c1"); got != "job-main" {
		t.Fatalf("main resolve = %q, want job-main", got)
	}
}

// TestWaitKinds: kinds accepts only "ask.reply" and rejects other values.
func TestWaitKinds(t *testing.T) {
	cases := []struct {
		name    string
		input   map[string]any
		kinds   []string
		wantErr string
	}{
		{name: "absent", input: map[string]any{}},
		{name: "ask.reply", input: map[string]any{"kinds": []any{"ask.reply"}}, kinds: []string{"ask.reply"}},
		{name: "unknown kind", input: map[string]any{"kinds": []any{"bash"}}, kinds: []string{"bash"}, wantErr: `kinds: unknown kind "bash", the only allowed kind is "ask.reply"`},
		{name: "not an array", input: map[string]any{"kinds": "ask.reply"}, wantErr: "kinds must be an array of strings"},
		{name: "not strings", input: map[string]any{"kinds": []any{1}}, wantErr: "kinds must contain only strings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kinds, errText := waitKinds(c.input)
			if errText != c.wantErr {
				t.Fatalf("error = %q, want %q", errText, c.wantErr)
			}
			if len(kinds) != len(c.kinds) {
				t.Fatalf("kinds = %v, want %v", kinds, c.kinds)
			}
		})
	}
}
