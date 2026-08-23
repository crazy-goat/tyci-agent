package main

import (
	"context"
	"testing"

	"github.com/decodo/tyci/jobs"
)

func TestParseMsgCommand(t *testing.T) {
	cases := []struct {
		arg      string
		wantJob  string
		wantText string
		wantErr  bool
	}{
		{"#3 stop and do X", "#3", "stop and do X", false},
		{"job-1-1 hello there", "job-1-1", "hello there", false},
		{"#3   spaced   text  ", "#3", "spaced   text", false},
		{"#3", "", "", true},        // no text
		{"", "", "", true},          // nothing at all
		{"onlyjobid", "", "", true}, // still just one field
	}
	for _, c := range cases {
		gotJob, gotText, err := parseMsgCommand(c.arg)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMsgCommand(%q): expected error, got job=%q text=%q", c.arg, gotJob, gotText)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMsgCommand(%q): unexpected error: %v", c.arg, err)
			continue
		}
		if gotJob != c.wantJob || gotText != c.wantText {
			t.Errorf("parseMsgCommand(%q) = (%q, %q), want (%q, %q)", c.arg, gotJob, gotText, c.wantJob, c.wantText)
		}
	}
}

// newRunningTestJob starts a job on reg that blocks until release is
// closed, for tests that need a real, resolvable, running job id.
func newRunningTestJob(reg *jobs.Registry, release <-chan struct{}) *jobs.Job {
	return reg.Start(context.Background(), "test job", func(ctx context.Context, _ string) (string, bool, error) {
		<-release
		return "done", false, nil
	})
}

// TestPostMsgCommand_ResolvesFullID: "/msg <full-id> <text>" posts to the
// exact job.
func TestPostMsgCommand_ResolvesFullID(t *testing.T) {
	reg := jobs.NewRegistry()
	release := make(chan struct{})
	defer close(release)
	job := newRunningTestJob(reg, release)

	gotID, err := postMsgCommand(reg, job.ID+" hello there")
	if err != nil {
		t.Fatalf("postMsgCommand: unexpected error: %v", err)
	}
	if gotID != job.ID {
		t.Fatalf("postMsgCommand returned job id %q, want %q", gotID, job.ID)
	}
	msgs := reg.DrainMessages(job.ID)
	if len(msgs) != 1 || msgs[0] != "hello there" {
		t.Fatalf("DrainMessages = %v, want [hello there]", msgs)
	}
}

// TestPostMsgCommand_ResolvesShortID: "/msg #N <text>" — the form the jobs
// panel actually shows — resolves to the same job as its full id.
func TestPostMsgCommand_ResolvesShortID(t *testing.T) {
	reg := jobs.NewRegistry()
	release := make(chan struct{})
	defer close(release)
	job := newRunningTestJob(reg, release)
	short := "#" + jobs.ShortID(job.ID)

	gotID, err := postMsgCommand(reg, short+" steer this way")
	if err != nil {
		t.Fatalf("postMsgCommand: unexpected error: %v", err)
	}
	if gotID != job.ID {
		t.Fatalf("postMsgCommand(%q) resolved to %q, want %q", short, gotID, job.ID)
	}
	msgs := reg.DrainMessages(job.ID)
	if len(msgs) != 1 || msgs[0] != "steer this way" {
		t.Fatalf("DrainMessages = %v, want [steer this way]", msgs)
	}
}

// TestPostMsgCommand_UnknownJobFails covers both bad-input shapes: a job
// reference matching nothing in the registry, full or short.
func TestPostMsgCommand_UnknownJobFails(t *testing.T) {
	reg := jobs.NewRegistry()

	if _, err := postMsgCommand(reg, "job-does-not-exist-1 hi"); err == nil {
		t.Error("expected an error for an unknown full job id")
	}
	if _, err := postMsgCommand(reg, "#999 hi"); err == nil {
		t.Error("expected an error for an unknown short job id")
	}
}

func TestPostMsgCommand_MissingTextFails(t *testing.T) {
	reg := jobs.NewRegistry()
	release := make(chan struct{})
	defer close(release)
	job := newRunningTestJob(reg, release)

	if _, err := postMsgCommand(reg, job.ID); err == nil {
		t.Error("expected an error when no text is given")
	}
}
