package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/connector/connectortest"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// TestResumeTool_KeepsRolePolicyOfStashedJob covers a role job that a caller
// outside the role resumes with the chat tool "resume". The stashed run had
// the role policy and its worktree. The resumed run must keep both: a write
// outside the worktree is refused, and a relative write lands in the worktree.
func TestResumeTool_KeepsRolePolicyOfStashedJob(t *testing.T) {
	cases := []struct {
		name       string
		permission string
		// refusal is the text the refused write must carry in the next request.
		refusal string
		// inside is true when the relative write may land in the worktree.
		inside bool
	}{
		{"worktree-write", tools.PermissionWorktreeWrite, "outside the worktree of role", true},
		{"read-only", tools.PermissionReadOnly, `not allowed for role`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, _ := withTestWiring(t)
			resetResumableForTest(t)
			// The hook registry is process-wide. A pre_tool hook would block the write.
			t.Cleanup(hooks.SetForTesting(nil))
			tools.SetJobResumer(jobResumerAdapter{reg: reg})
			t.Cleanup(func() { tools.SetJobResumer(nil) })

			wt := t.TempDir()
			outside := t.TempDir()
			const name = "resume-policy-note.txt"
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Remove(filepath.Join(cwd, name)) })

			outsideFile := filepath.Join(outside, "out.txt")
			// Turn 0 answers the first task, turn 1 tries two writes, turn 2 ends.
			fake := &connectortest.Fake{Script: func(turn int, _ connector.Request) []stream.Event {
				switch turn {
				case 0:
					return []stream.Event{stream.TextDelta{Text: "first"}, stream.Finish{Reason: "stop"}}
				case 1:
					return []stream.Event{
						stream.ToolCall{ID: "tc-out", Name: "write", Arguments: `{"path":"` + outsideFile + `","content":"x"}`},
						stream.ToolCall{ID: "tc-in", Name: "write", Arguments: `{"path":"` + name + `","content":"note"}`},
						stream.Finish{Reason: "tool_calls"},
					}
				default:
					return []stream.Event{stream.TextDelta{Text: "done"}, stream.Finish{Reason: "stop"}}
				}
			}}

			// The role job runs and stashes its conversation, as a real job does.
			const jobID = "role-job-resume"
			ctx := connector.WithModelClient(context.Background(), fake)
			ctx = context.WithValue(ctx, tools.JobIDCtxKey{}, jobID)
			ctx = tools.WithPolicy(ctx, tools.Policy{Role: "coder", Permission: tc.permission, Dir: wt})
			ctx = tools.WithWorkdir(ctx, wt)
			if _, err := (&agentRunner{}).run(ctx, "first task", "", "", tools.SubagentOptions{}); err != nil {
				t.Fatalf("role run: %v", err)
			}

			// A caller without any role policy resumes the job with the chat tool.
			res := (&tools.ResumeTool{}).Run(context.Background(), map[string]any{"job_id": jobID, "task": "write the files"})
			if !res.Success {
				t.Fatalf("resume tool: %+v", res)
			}
			var id string
			for _, j := range reg.List() {
				if j.Description == "write the files" {
					id = j.ID
				}
			}
			if id == "" {
				t.Fatal("the resume tool started no job")
			}
			if _, ok := reg.Wait(context.Background(), id, 10*time.Second); !ok {
				t.Fatal("the resumed job never ended")
			}

			// The refusal is what the model saw, so the writes were stopped by the gate.
			reqs := fake.Requests()
			if len(reqs) < 3 {
				t.Fatalf("the resumed job made %d model calls, want 3", len(reqs))
			}
			b, err := json.Marshal(reqs[2])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), tc.refusal) {
				t.Fatalf("the model did not see the refusal %q in %s", tc.refusal, b)
			}

			if _, err := os.Stat(outsideFile); !os.IsNotExist(err) {
				t.Fatalf("the resumed job wrote outside its worktree (err = %v)", err)
			}
			_, inWt := os.Stat(filepath.Join(wt, name))
			_, inCwd := os.Stat(filepath.Join(cwd, name))
			if tc.inside && inWt != nil {
				t.Fatalf("the resumed job did not write in its worktree: %v", inWt)
			}
			if !tc.inside && inWt == nil {
				t.Fatal("the resumed read-only job wrote in its worktree")
			}
			if inCwd == nil {
				t.Fatal("the resumed job wrote in the process directory")
			}
		})
	}
}
