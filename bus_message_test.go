package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TestAgentMessage_ToLiveJob_DeliveredOnce posts a message to a live job. The
// job's inbox gets it once, and the orchestrator gets nothing.
func TestAgentMessage_ToLiveJob_DeliveredOnce(t *testing.T) {
	reg, _ := withTestWiring(t)
	id, release := startLiveJob(t, reg)
	defer release()

	if err := (jobMailboxAdapter{reg: reg}).Post("", id, "steer left"); err != nil {
		t.Fatalf("Post to a live job: %v", err)
	}

	if got := (jobMailboxAdapter{reg: reg}).Drain(id); len(got) != 1 || bodyOf(got[0]) != "steer left" {
		t.Fatalf("drain = %q, want the message once", got)
	}
	if again := (jobMailboxAdapter{reg: reg}).Drain(id); len(again) != 0 {
		t.Fatalf("second drain = %q, want nothing", again)
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAgentMessage_ToFinishedJob_Refused posts to a job that is not live. Post
// reports false, and nothing is published for it.
func TestAgentMessage_ToFinishedJob_Refused(t *testing.T) {
	reg, _ := withTestWiring(t)
	if (jobMailboxAdapter{reg: reg}).Post("", "job-not-started", "too late") == nil {
		t.Fatal("Post to an unknown job returned nil")
	}
	if got := drainNotices(); len(got) != 0 {
		t.Fatalf("orchestrator drain = %q, want nothing", got)
	}
}

// TestAgentMessage_PublishErrorIsLoggedNotFatal publishes a message on a closed
// bus. The call must not panic, and the error must be logged.
func TestAgentMessage_PublishErrorIsLoggedNotFatal(t *testing.T) {
	withTestWiring(t)
	var log bytes.Buffer
	orig := busLog
	busLog = &log
	t.Cleanup(func() { busLog = orig })

	closed := newAppBus("")
	closed.Close()
	publishAgentMessage(closed, "job-closed", orchestratorAddr, bus.OriginAgent, "hi")

	if !strings.Contains(log.String(), "not published") {
		t.Fatalf("log = %q, want the publish error", log.String())
	}
}

// startJobUnder starts a live job whose parent is parentID, and returns its id
// and a function that ends it.
func startJobUnder(t *testing.T, reg *jobs.Registry, parentID string) (string, func()) {
	t.Helper()
	release := make(chan struct{})
	job := reg.Start(context.Background(), "agent", jobs.KindSubagent, parentID, func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "", false, nil
	})
	return job.ID, func() { close(release) }
}

// sendAs runs the "message" tool as job from ("" is the orchestrator) and
// returns its result.
func sendAs(t *testing.T, from, to, text string) tools.ToolResult {
	t.Helper()
	ctx := context.Background()
	if from != "" {
		ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, from)
	}
	return (&tools.MessageTool{}).Run(ctx, map[string]any{"job_id": to, "text": text})
}

// inboxTexts drains the inbox of job id and returns the bodies of its messages.
func inboxTexts(id string) []string {
	var out []string
	for _, s := range agentInboxes.drain(id) {
		out = append(out, bodyOf(s))
	}
	return out
}

// setupMessageRoutes wires the message tool to reg and clears the wiring at
// the end of the test.
func setupMessageRoutes(t *testing.T, reg *jobs.Registry) {
	t.Helper()
	tools.SetJobMailbox(jobMailboxAdapter{reg: reg})
	t.Cleanup(func() { tools.SetJobMailbox(nil) })
}

// TestAgentMessage_ToParent_Delivered: a child sends to its parent, and the
// parent's inbox gets the message.
func TestAgentMessage_ToParent_Delivered(t *testing.T) {
	reg, _ := withTestWiring(t)
	setupMessageRoutes(t, reg)
	parent, releaseParent := startJobUnder(t, reg, "")
	defer releaseParent()
	child, releaseChild := startJobUnder(t, reg, parent)
	defer releaseChild()

	if res := sendAs(t, child, parent, "done with step one"); !res.Success {
		t.Fatalf("child to parent: error %q", res.Error)
	}
	if got := inboxTexts(parent); len(got) != 1 || got[0] != "done with step one" {
		t.Fatalf("parent inbox = %q, want the message once", got)
	}
}

// TestAgentMessage_ToSibling_Refused: an agent may not message a sibling. The
// tool returns the rule as its error, and the sibling gets nothing.
func TestAgentMessage_ToSibling_Refused(t *testing.T) {
	reg, _ := withTestWiring(t)
	setupMessageRoutes(t, reg)
	parent, releaseParent := startJobUnder(t, reg, "")
	defer releaseParent()
	a, releaseA := startJobUnder(t, reg, parent)
	defer releaseA()
	b, releaseB := startJobUnder(t, reg, parent)
	defer releaseB()

	res := sendAs(t, a, b, "hello sibling")
	if res.Success || res.Error != tools.ErrNotParent.Error() {
		t.Fatalf("sibling message: success=%v error=%q, want the parent rule", res.Success, res.Error)
	}
	if got := inboxTexts(b); len(got) != 0 {
		t.Fatalf("sibling inbox = %q, want nothing", got)
	}
}

// TestAgentMessage_ToChildFromParent_Delivered: the orchestrator and a parent
// job both reach a child.
func TestAgentMessage_ToChildFromParent_Delivered(t *testing.T) {
	reg, _ := withTestWiring(t)
	setupMessageRoutes(t, reg)
	parent, releaseParent := startJobUnder(t, reg, "")
	defer releaseParent()
	child, releaseChild := startJobUnder(t, reg, parent)
	defer releaseChild()

	if res := sendAs(t, "", child, "from the orchestrator"); !res.Success {
		t.Fatalf("orchestrator to child: error %q", res.Error)
	}
	if res := sendAs(t, parent, child, "from the parent"); !res.Success {
		t.Fatalf("parent to child: error %q", res.Error)
	}
	if got := inboxTexts(child); len(got) != 2 {
		t.Fatalf("child inbox = %q, want both messages", got)
	}
}

// TestAgentMessage_FromUser_Delivered: a /msg from the person reaches a job
// whose parent is not the orchestrator.
func TestAgentMessage_FromUser_Delivered(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, releaseParent := startJobUnder(t, reg, "")
	defer releaseParent()
	child, releaseChild := startJobUnder(t, reg, parent)
	defer releaseChild()

	if _, err := postMsgCommand(appBus, reg, child+" please stop"); err != nil {
		t.Fatalf("/msg: %v", err)
	}
	if got := inboxTexts(child); len(got) != 1 || got[0] != "please stop" {
		t.Fatalf("child inbox = %q, want the user message", got)
	}
}

// TestPost_UsesRealSender: the message from a child to its parent carries the
// child as its sender, not the orchestrator.
func TestPost_UsesRealSender(t *testing.T) {
	reg, _ := withTestWiring(t)
	parent, releaseParent := startJobUnder(t, reg, "")
	defer releaseParent()
	child, releaseChild := startJobUnder(t, reg, parent)
	defer releaseChild()

	if err := (jobMailboxAdapter{reg: reg}).Post(child, parent, "hi"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	got := agentInboxes.drain(parent)
	want := "[notice from=agent:" + child + " kind=agent.message]"
	if len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Fatalf("parent inbox = %q, want a message from %s", got, child)
	}
}
