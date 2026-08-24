package tools

import (
	"context"
	"strings"
	"testing"
)

// fakeJobMailbox is a minimal JobMailbox for exercising MessageTool.Run and
// JobMailboxNextMessages without a real jobs.Registry.
type fakeJobMailbox struct {
	resolved  map[string]string // idOrShort -> full id
	posts     []postCall
	postFails map[string]bool // full id -> Post returns false
	live      map[string]bool
	drainOut  map[string][]string
}

type postCall struct {
	id, text string
}

func (f *fakeJobMailbox) Resolve(id string) (string, bool) {
	full, ok := f.resolved[id]
	return full, ok
}

func (f *fakeJobMailbox) IsLive(id string) bool {
	return f.live == nil || f.live[id]
}

func (f *fakeJobMailbox) Post(id, text string) bool {
	if f.postFails[id] {
		return false
	}
	f.posts = append(f.posts, postCall{id, text})
	return true
}

func (f *fakeJobMailbox) Drain(id string) []string {
	return f.drainOut[id]
}

func withFakeMailbox(t *testing.T, f *fakeJobMailbox) {
	old := jobMailbox
	t.Cleanup(func() { jobMailbox = old })
	jobMailbox = f
}

func TestMessageTool_RequiresJobID(t *testing.T) {
	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"text": "hi"})
	if res.Success {
		t.Fatal("expected failure without job_id")
	}
}

func TestMessageTool_RequiresText(t *testing.T) {
	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1-1"})
	if res.Success {
		t.Fatal("expected failure without text")
	}
}

func TestMessageTool_UnavailableWithoutMailbox(t *testing.T) {
	old := jobMailbox
	jobMailbox = nil
	defer func() { jobMailbox = old }()

	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1-1", "text": "hi"})
	if res.Success {
		t.Fatal("expected failure when no mailbox is wired")
	}
}

// TestMessageTool_ResolvesShortID: the model may pass a short "#N" job id
// (as shown in the jobs panel) — MessageTool must resolve it through
// JobMailbox.Resolve before posting, exactly like the "/msg" slash command.
func TestMessageTool_ResolvesShortID(t *testing.T) {
	f := &fakeJobMailbox{resolved: map[string]string{"#7": "job-12345-7"}}
	withFakeMailbox(t, f)

	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "#7", "text": "stop and do X instead"})
	if !res.Success {
		t.Fatalf("expected success, got error %q", res.Error)
	}
	if len(f.posts) != 1 {
		t.Fatalf("expected exactly one Post call, got %v", f.posts)
	}
	if f.posts[0].id != "job-12345-7" || f.posts[0].text != "stop and do X instead" {
		t.Errorf("Post call = %+v, want id=job-12345-7 text=%q", f.posts[0], "stop and do X instead")
	}
}

func TestMessageTool_UnknownJobIDFails(t *testing.T) {
	f := &fakeJobMailbox{resolved: map[string]string{}}
	withFakeMailbox(t, f)

	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-does-not-exist", "text": "hi"})
	if res.Success {
		t.Fatal("expected failure for a job id that doesn't resolve")
	}
	if len(f.posts) != 0 {
		t.Errorf("expected no Post call for an unresolved job id, got %v", f.posts)
	}
}

func TestMessageTool_TerminalJobFailsWithResumeGuidance(t *testing.T) {
	f := &fakeJobMailbox{resolved: map[string]string{"done": "done"}, live: map[string]bool{"done": false}}
	withFakeMailbox(t, f)
	res := (&MessageTool{}).Run(context.Background(), map[string]any{"job_id": "done", "text": "hi"})
	if res.Success || !strings.Contains(res.Error, "resume(job_id, task)") || !strings.Contains(res.Error, "only targets live jobs") {
		t.Fatalf("error = %q, want live-job/resume guidance", res.Error)
	}
	if len(f.posts) != 0 {
		t.Fatalf("terminal job received post: %v", f.posts)
	}
}

func TestMessageTool_PostFailureIsReported(t *testing.T) {
	f := &fakeJobMailbox{
		resolved:  map[string]string{"job-1-1": "job-1-1"},
		postFails: map[string]bool{"job-1-1": true},
	}
	withFakeMailbox(t, f)

	tool := &MessageTool{}
	res := tool.Run(context.Background(), map[string]any{"job_id": "job-1-1", "text": "hi"})
	if res.Success {
		t.Fatal("expected failure when Post itself reports false")
	}
}

// TestJobMailboxNextMessages_DrainsBoundJob: the callback returned by
// JobMailboxNextMessages must drain exactly the jobID it was built with —
// this is the piece wired into a background subagent's own
// agent.Config.NextMessages, mirroring how the main agent drains its own
// pending-input queue.
func TestJobMailboxNextMessages_DrainsBoundJob(t *testing.T) {
	f := &fakeJobMailbox{drainOut: map[string][]string{
		"job-1-1": {"steer this way"},
		"job-2-2": {"unrelated"},
	}}
	withFakeMailbox(t, f)

	next := JobMailboxNextMessages("job-1-1")
	if next == nil {
		t.Fatal("expected a non-nil callback when a mailbox is wired and jobID is non-empty")
	}
	got := next()
	if len(got) != 1 || got[0] != "steer this way" {
		t.Fatalf("next() = %v, want [steer this way]", got)
	}
}

func TestJobMailboxNextMessages_NilWithoutMailboxOrJobID(t *testing.T) {
	old := jobMailbox
	jobMailbox = nil
	defer func() { jobMailbox = old }()

	if next := JobMailboxNextMessages("job-1-1"); next != nil {
		t.Error("expected nil callback when no mailbox is wired")
	}

	withFakeMailbox(t, &fakeJobMailbox{})
	if next := JobMailboxNextMessages(""); next != nil {
		t.Error("expected nil callback for an empty jobID")
	}
}

// TestMessageTool_DeniedToSubagentSchema mirrors
// TestGetSubagentToolsSchemaJSONFor_EmptyReturnsFullSchema's answer_job
// assertion: a plain subagent child can never have a job of its own to
// message (it is always denied "subagent"), so "message" must never appear
// in the schema offered to one.
func TestMessageTool_DeniedToSubagentSchema(t *testing.T) {
	names := schemaToolNames(t, GetSubagentToolsSchemaJSON())
	if names["message"] {
		t.Error(`subagent schema must never include "message" — a plain child cannot spawn further children, so it can never have a job of its own to message`)
	}
}

// TestMessageTool_AvailableToTopLevelSchema: the main agent (and /btw,
// which uses GetAllToolsSchemaJSON) must still be able to message its own
// children.
func TestMessageTool_AvailableToTopLevelSchema(t *testing.T) {
	names := schemaToolNames(t, GetTopLevelToolsSchemaJSON())
	if !names["message"] {
		t.Error(`expected the top-level tool schema to include "message"`)
	}
}

func TestMessageTool_IsSubagentDenied(t *testing.T) {
	if !IsSubagentDenied("message") {
		t.Error(`expected IsSubagentDenied("message") to be true`)
	}
}

func TestMessageTool_TerminalStatusesRequireResume(t *testing.T) {
	for _, status := range []string{"done", "failed", "truncated"} {
		t.Run(status, func(t *testing.T) {
			f := &fakeJobMailbox{
				resolved: map[string]string{"#7": "job-12345-7"},
				live:     map[string]bool{"job-12345-7": false},
			}
			withFakeMailbox(t, f)

			res := (&MessageTool{}).Run(context.Background(), map[string]any{
				"job_id": "#7",
				"text":   "continue this",
			})
			if res.Success {
				t.Fatalf("%s job unexpectedly accepted a message", status)
			}
			if !strings.Contains(res.Error, "only targets live jobs") || !strings.Contains(res.Error, "resume(job_id, task)") || !strings.Contains(res.Error, "resume(job_id=\"job-12345-7\"") {
				t.Fatalf("error = %q, want live-job/resume guidance for %s", res.Error, status)
			}
			if len(f.posts) != 0 {
				t.Fatalf("%s job received post: %v", status, f.posts)
			}
		})
	}
}

func TestMessageTool_UnknownShortIDFailsWithoutPost(t *testing.T) {
	f := &fakeJobMailbox{resolved: map[string]string{"#7": "job-12345-7"}}
	withFakeMailbox(t, f)

	res := (&MessageTool{}).Run(context.Background(), map[string]any{
		"job_id": "#99",
		"text":   "hi",
	})
	if res.Success {
		t.Fatal("expected failure for an unknown short job id")
	}
	if !strings.Contains(res.Error, "unknown job_id") || !strings.Contains(res.Error, "cannot be resumed") {
		t.Fatalf("error = %q, want unknown-job guidance", res.Error)
	}
	if len(f.posts) != 0 {
		t.Fatalf("unknown job received post: %v", f.posts)
	}
}
