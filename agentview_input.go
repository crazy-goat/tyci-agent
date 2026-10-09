package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/crazy-goat/tyci-agent/internal/flow"
	"github.com/crazy-goat/tyci-agent/internal/ledger"
	"github.com/crazy-goat/tyci-agent/internal/pricing"
	"github.com/crazy-goat/tyci-agent/stream"
	"github.com/crazy-goat/tyci-agent/tools"
)

// agentViewInput is the app side of the input of the agent view (see
// display.AgentInput). The display calls it from a tea.Cmd, never from Update.
type agentViewInput struct{}

// Post puts text into the mailbox of the running agent jobID, the same way
// "/msg" does.
func (agentViewInput) Post(jobID, text string) error {
	if !JobRegistry.IsLive(jobID) {
		return fmt.Errorf("job %s is not running", jobID)
	}
	return publishUserMessage(appBus, jobID, text)
}

// ResumeCheck returns the context size (estimated in tokens) and the estimated
// cost of resuming the finished agent jobID. The cost is the input price of
// the whole context, without the cache discount: a resume loads the context
// again. priced is false when the model has no known price; then usd is not
// an estimate. It returns an error when jobID cannot be resumed.
func (agentViewInput) ResumeCheck(jobID string) (tokens int, usd float64, priced bool, err error) {
	if _, err := agentRunWorkdir(jobID); err != nil {
		return 0, 0, false, err
	}
	resumableMu.Lock()
	entry, ok := resumable[jobID]
	resumableMu.Unlock()
	if !ok {
		return 0, 0, false, fmt.Errorf("job %s has no saved conversation to resume", jobID)
	}
	// The same estimate as the status bar: about four bytes per token.
	data, _ := json.Marshal(entry.msgs)
	tokens = len(data) / 4
	rates, _ := pricing.Lookup(entry.mc.Provider(), entry.mc.Model())
	return tokens, ledger.Cost(rates, stream.Usage{Input: tokens}), rates.Known(), nil
}

// Resume starts a new job that continues the conversation of the finished
// agent jobID with text, and returns the id of the new job. The new job is a
// side conversation: nothing waits for it and its reply goes nowhere else.
func (agentViewInput) Resume(jobID, text string) (string, error) {
	workdir, err := agentRunWorkdir(jobID)
	if err != nil {
		return "", err
	}
	ctx := context.WithValue(context.Background(), tools.AskUnroutableCtxKey{}, true)
	if workdir != "" {
		ctx = tools.WithWorkdir(ctx, workdir)
	}
	h, err := jobResumerAdapter{reg: JobRegistry}.Resume(ctx, jobID, text)
	if err != nil {
		return "", err
	}
	return h.ID(), nil
}

// agentRunInfo gives the repository info for the run lookup. Tests replace it.
var agentRunInfo = func() (flow.RepoInfo, error) { return workflowManager.Info() }

// agentRunWorkdir refuses a resume of an agent that belongs to a workflow run
// which is not done or paused. It returns the worktree of that run, so the
// resumed agent works in the same files. An agent of no run returns "". A
// resumed job belongs to the run of the agent it continues (see
// runIDsOf). The worktree of a merged or skipped run is removed; a resume
// then would work in the main checkout, so it is refused too.
func agentRunWorkdir(jobID string) (string, error) {
	info, err := agentRunInfo()
	if err != nil {
		// Without repository info no workflow run can start in this
		// directory (Start, StartIssue and Resume need it too), so the agent
		// belongs to no run.
		return "", nil
	}
	return runWorkdirIn(info, jobID)
}

// runIDsOf returns the job ids that can be the session of a step of the run
// of jobID: every id of each conversation chain that jobID belongs to. A run
// records the last job of a chain, so the first job of the chain is not enough.
func runIDsOf(jobID string) []string {
	resumableMu.Lock()
	defer resumableMu.Unlock()
	mine := chainIDs(jobID, resumable[jobID])
	want := map[string]bool{}
	for _, id := range mine {
		want[id] = true
	}
	ids := append([]string(nil), mine...)
	for id, entry := range resumable {
		for _, c := range chainIDs(id, entry) {
			if want[c] {
				ids = append(ids, id)
				break
			}
		}
	}
	return ids
}

// chainIDs returns the chain of the job id whose stashed entry is e.
func chainIDs(id string, e resumableEntry) []string {
	if len(e.chain) == 0 {
		return []string{id}
	}
	return e.chain
}

// runWorkdirIn is agentRunWorkdir for the repository info info.
func runWorkdirIn(info flow.RepoInfo, jobID string) (string, error) {
	st, ok, err := flow.RunOfSession(info.Home, info.Name(), runIDsOf(jobID))
	if err != nil {
		return "", fmt.Errorf("cannot check the workflow runs for the agent: %w", err)
	}
	if !ok {
		return "", nil
	}
	if st.Status != "done" && st.Status != "paused" {
		return "", fmt.Errorf("the agent belongs to run %s (%s): only agents of done or paused runs can be resumed", st.Run, st.Status)
	}
	if _, err := os.Stat(st.Worktree); err != nil {
		return "", fmt.Errorf("the worktree of run %s is gone: the agent cannot work in its files", st.Run)
	}
	return st.Worktree, nil
}
