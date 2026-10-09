package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/pricing"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/stream"
)

// saveTestRun writes a workflow run of repo "repo" under home, with one agent
// step that ran as session job.
func saveTestRun(t *testing.T, home, run, status, worktree, job string) {
	t.Helper()
	dir := filepath.Join(home, ".tyci", "runs", "repo", run)
	st := &flow.RunState{Version: 1, Run: run, Status: status, Worktree: worktree,
		History: []flow.Step{{Seq: 1, Kind: "agent", Session: job}}}
	if err := (&flow.Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
}

func TestRunWorkdirIn_RefusesAgentsOfActiveRuns(t *testing.T) {
	home := t.TempDir()
	wt := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}

	saveTestRun(t, home, "run-active", "running", wt, "job-active")
	_, err := runWorkdirIn(info, "job-active")
	if err == nil || !strings.Contains(err.Error(), "run-active (running)") {
		t.Fatalf("err = %v, want a refusal that names the active run", err)
	}

	saveTestRun(t, home, "run-paused", "paused", wt, "job-paused")
	if dir, err := runWorkdirIn(info, "job-paused"); err != nil || dir != wt {
		t.Fatalf("paused run: dir=%q err=%v, want the run worktree", dir, err)
	}
}

func TestRunWorkdirIn_RefusesWhenTheWorktreeIsGone(t *testing.T) {
	home := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}
	gone := filepath.Join(t.TempDir(), "removed")
	saveTestRun(t, home, "run-done", "done", gone, "job-done")

	if _, err := runWorkdirIn(info, "job-done"); err == nil || !strings.Contains(err.Error(), "worktree of run run-done is gone") {
		t.Fatalf("err = %v, want a refusal for the missing worktree", err)
	}
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if dir, err := runWorkdirIn(info, "job-done"); err != nil || dir != gone {
		t.Fatalf("dir=%q err=%v, want the worktree once it exists", dir, err)
	}
}

func TestRunWorkdirIn_AgentOfNoRunIsAllowed(t *testing.T) {
	info := flow.RepoInfo{Home: t.TempDir(), Repo: "owner/repo"}
	if dir, err := runWorkdirIn(info, "job-btw"); err != nil || dir != "" {
		t.Fatalf("dir=%q err=%v, want no worktree and no error", dir, err)
	}
}

// startNamedJob starts a subagent job with the description name in a fresh
// JobRegistry (withTestWiring drains its events at the end of the test). The
// job runs until the test ends and returns its id.
func startNamedJob(t *testing.T, name string) string {
	t.Helper()
	withTestWiring(t)
	block := make(chan struct{})
	job := JobRegistry.Start(context.Background(), name, jobs.KindSubagent, "", func(context.Context, string) (string, bool, error) {
		<-block
		return "", false, nil
	})
	t.Cleanup(func() {
		close(block)
		JobRegistry.Cancel(job.ID)
	})
	return job.ID
}

func TestRunWorkdirIn_RefusesAWorkflowAgentOfAnActiveRunBeforeItsStepIsSaved(t *testing.T) {
	// The run has no step of this job yet (a report reminder runs before the
	// step is saved). The job name "<run>/<role>" still finds the run.
	home := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}
	saveTestRun(t, home, "run-active", "running", t.TempDir(), "job-other")
	job := startNamedJob(t, "run-active/coder")

	if _, err := runWorkdirIn(info, job); err == nil || !strings.Contains(err.Error(), "run-active (running)") {
		t.Fatalf("err = %v, want a refusal that names the active run", err)
	}
}

func TestRunWorkdirIn_AllowsAWorkflowAgentOfADoneRunBeforeItsStepIsSaved(t *testing.T) {
	home := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}
	wt := t.TempDir()
	saveTestRun(t, home, "run-done", "done", wt, "job-other")
	job := startNamedJob(t, "run-done/coder")

	if dir, err := runWorkdirIn(info, job); err != nil || dir != wt {
		t.Fatalf("dir=%q err=%v, want the worktree of the done run", dir, err)
	}
}

func TestRunWorkdirIn_PlainJobNameIsNoRun(t *testing.T) {
	// "foo/bar" names no run: there is no state file of run foo, so the
	// agent belongs to no run and the resume is allowed.
	home := t.TempDir()
	info := flow.RepoInfo{Home: home, Repo: "owner/repo"}
	job := startNamedJob(t, "foo/bar")

	if dir, err := runWorkdirIn(info, job); err != nil || dir != "" {
		t.Fatalf("dir=%q err=%v, want no worktree and no error", dir, err)
	}
}

func TestPostToLiveAgent_IsRefusedForAFinishedJob(t *testing.T) {
	// A job that the registry does not know is not live: Post must refuse it
	// instead of publishing into a dead mailbox.
	if err := (agentViewInput{}).Post("job-unknown", "hi"); err == nil {
		t.Fatal("Post to a job that is not running must fail")
	}
}

// fakeCheckModel is a model client with a fixed provider and model name. It
// never streams: ResumeCheck only reads its names.
type fakeCheckModel struct{ provider, model string }

func (f fakeCheckModel) Provider() string { return f.provider }
func (f fakeCheckModel) Model() string    { return f.model }
func (f fakeCheckModel) Stream(context.Context, connector.Request) (<-chan stream.Event, error) {
	return nil, errors.New("not used by the resume check")
}

// useRunInfo replaces the run lookup for one test: it returns the repository
// with home as its home, or err.
func useRunInfo(t *testing.T, home string, err error) {
	t.Helper()
	orig := agentRunInfo
	agentRunInfo = func() (flow.RepoInfo, error) {
		return flow.RepoInfo{Home: home, Repo: "owner/repo"}, err
	}
	t.Cleanup(func() { agentRunInfo = orig })
}

// stashCheckAgent saves a finished agent job with a conversation of about
// 400 bytes, priced by model client mc.
func stashCheckAgent(t *testing.T, jobID string, mc connector.ModelClient) []connector.Message {
	t.Helper()
	resetResumableForTest(t)
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: strings.Repeat("x", 400)}}}}
	stashResumable(jobID, resumableEntry{msgs: msgs, mc: mc})
	return msgs
}

func TestResumeCheck_EstimatesTokensFromTheSavedConversation(t *testing.T) {
	useRunInfo(t, t.TempDir(), nil)
	msgs := stashCheckAgent(t, "job-check", fakeCheckModel{provider: "nowhere", model: "unpriced"})

	tokens, _, _, err := (agentViewInput{}).ResumeCheck("job-check")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(msgs)
	if want := len(data) / 4; tokens != want || tokens < 100 {
		t.Fatalf("tokens = %d, want %d (about four bytes per token of the saved conversation)", tokens, want)
	}
}

func TestResumeCheck_UnpricedModelReportsNoCost(t *testing.T) {
	useRunInfo(t, t.TempDir(), nil)
	stashCheckAgent(t, "job-unpriced", fakeCheckModel{provider: "nowhere", model: "unpriced"})

	_, usd, priced, err := (agentViewInput{}).ResumeCheck("job-unpriced")
	if err != nil {
		t.Fatal(err)
	}
	if priced || usd != 0 {
		t.Fatalf("priced=%v usd=%v, want an unpriced model with no cost estimate", priced, usd)
	}
}

func TestResumeCheck_PricedModelReportsTheInputCost(t *testing.T) {
	// The price catalog is read from HOME/.tyci/providers.json.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog := `{"anthropic":{"id":"anthropic","npm":"@ai-sdk/anthropic","name":"Anthropic","models":{
		"claude-sonnet-5":{"id":"claude-sonnet-5","name":"Claude Sonnet 5",
		"cost":{"input":3,"output":15},"limit":{"context":200000,"output":64000}}}}}`
	if err := os.WriteFile(filepath.Join(home, ".tyci", "providers.json"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	pricing.Reset()
	t.Cleanup(pricing.Reset)
	useRunInfo(t, t.TempDir(), nil)
	stashCheckAgent(t, "job-priced", fakeCheckModel{provider: "anthropic", model: "claude-sonnet-5"})

	tokens, usd, priced, err := (agentViewInput{}).ResumeCheck("job-priced")
	if err != nil {
		t.Fatal(err)
	}
	want := float64(tokens) / 1_000_000 * 3
	if !priced || usd <= 0 || usd < want*0.999 || usd > want*1.001 {
		t.Fatalf("priced=%v usd=%v tokens=%d, want the input price of the tokens (%v)", priced, usd, tokens, want)
	}
}

func TestAgentRunWorkdir_AllowsAResumeWhenTheRepositoryCannotBeRead(t *testing.T) {
	// No repository means no workflow run can exist in this directory, so the
	// agent belongs to no run and the resume goes ahead without a worktree.
	useRunInfo(t, "", errors.New("not a git repository"))
	stashCheckAgent(t, "job-lookup", fakeCheckModel{provider: "nowhere", model: "unpriced"})

	if dir, err := agentRunWorkdir("job-lookup"); err != nil || dir != "" {
		t.Fatalf("dir=%q err=%v, want no worktree and no error", dir, err)
	}
	if _, _, _, err := (agentViewInput{}).ResumeCheck("job-lookup"); err != nil {
		t.Fatalf("ResumeCheck err = %v, want the resume allowed", err)
	}
}

func TestAgentRunWorkdir_RefusesWhenARunCannotBeRead(t *testing.T) {
	// The agent may belong to a run whose state cannot be read, so refuse.
	home := t.TempDir()
	broken := filepath.Join(home, ".tyci", "runs", "repo", "run-broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "state.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	useRunInfo(t, home, nil)

	if _, err := agentRunWorkdir("job-unknown"); err == nil {
		t.Fatal("a resume must be refused while a run of the agent cannot be read")
	}
}

// stashChainedAgent stashes a resumed job whose chain starts at origin.
func stashChainedAgent(jobID, origin string) {
	msgs := []connector.Message{{Role: "user", Content: []connector.ContentBlock{{Type: "text", Text: "go on"}}}}
	stashResumable(jobID, resumableEntry{msgs: msgs, mc: fakeCheckModel{provider: "nowhere", model: "unpriced"}, chain: []string{origin, jobID}})
}

func TestAgentRunWorkdir_ChainedResumeOfAnActiveRunIsRefused(t *testing.T) {
	// The resumed job is not in the run history: its run is found by the
	// origin, so the run guard still refuses it.
	resetResumableForTest(t)
	home := t.TempDir()
	saveTestRun(t, home, "run-active", "running", t.TempDir(), "job-orig")
	useRunInfo(t, home, nil)
	stashChainedAgent("job-chain", "job-orig")

	if _, err := agentRunWorkdir("job-chain"); err == nil || !strings.Contains(err.Error(), "run-active (running)") {
		t.Fatalf("err = %v, want a refusal that names the active run", err)
	}
}

func TestAgentRunWorkdir_ChainedResumeOfADoneRunGetsTheWorktree(t *testing.T) {
	resetResumableForTest(t)
	home := t.TempDir()
	wt := t.TempDir()
	saveTestRun(t, home, "run-done", "done", wt, "job-orig2")
	useRunInfo(t, home, nil)
	stashChainedAgent("job-chain2", "job-orig2")

	if dir, err := agentRunWorkdir("job-chain2"); err != nil || dir != wt {
		t.Fatalf("dir=%q err=%v, want the worktree of the done run", dir, err)
	}
}

func TestResumeChain_KeepsEveryIdOfTheChain(t *testing.T) {
	if got := resumeChain(resumableEntry{}, "job-first", "job-second"); len(got) != 2 || got[0] != "job-first" || got[1] != "job-second" {
		t.Fatalf("first resume chain = %v, want [job-first job-second]", got)
	}
	entry := resumableEntry{chain: []string{"job-first", "job-second"}}
	if got := resumeChain(entry, "job-second", "job-third"); len(got) != 3 || got[2] != "job-third" || got[0] != "job-first" {
		t.Fatalf("chained resume chain = %v, want [job-first job-second job-third]", got)
	}
}
