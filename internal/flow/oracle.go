package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// defaultOracleAnswers is the oracle answer limit of a workflow without
// defaults.oracle_answers.
const defaultOracleAnswers = 2

// oracleTask is the task template of the oracle answer to an ask pause.
const oracleTask = "ask"

// askOrPause pauses the run in the ask state s with the pause message. Unless
// a human must answer, the oracle answers the pause first (see oracleAnswer).
// A valid answer moves the run on, and askOrPause returns the result of the rest
// of the run. The run stays "running" while the oracle works.
func (r *Runner) askOrPause(ctx context.Context, st *RunState, s State, message, reason string) error {
	r.setAsk(st, message, reason)
	if !r.oracleMayAnswer(st, s) {
		return r.savePaused(st)
	}
	return r.oracleAnswer(ctx, st, s)
}

// oracleMayAnswer reports whether the oracle may answer the pause in st, which
// sits in the ask state s. A human always answers when the state says "human",
// when the pause holds a workflow proposal, when the run stopped at a protected
// merge, and when the oracle already sent the run to ask. Past the limit, the
// oracle does not answer.
func (r *Runner) oracleMayAnswer(st *RunState, s State) bool {
	if r.Agents == nil || s.Human || st.Ask == nil || st.Ask.Proposal != "" {
		return false
	}
	if n := len(st.History); n > 0 {
		last := st.History[n-1]
		if last.Role == "oracle" || (last.State == "merge" && last.Key == "protected") {
			return false
		}
	}
	return oracleCount(st) < r.oracleLimit()
}

// oracleLimit is the number of oracle answers one run may use.
func (r *Runner) oracleLimit() int {
	if n := r.WF.Defaults.OracleAnswers; n != nil {
		return *n
	}
	return defaultOracleAnswers
}

// oracleCount counts the oracle answers of the run. An escalated answer counts too.
func oracleCount(st *RunState) int {
	n := 0
	for _, h := range st.History {
		if h.Kind == "ask" && h.Role == "oracle" {
			n++
		}
	}
	return n
}

// oracleAnswer asks the oracle for the pause of st, which sits in the ask state s.
// A valid answer moves the run on, as a human answer does (see Resume). Any other
// answer, or an oracle error, leaves the run paused for a human. The answer is
// the first line of the oracle text, the reason the other lines.
func (r *Runner) oracleAnswer(ctx context.Context, st *RunState, s State) error {
	cur := st.Current
	step := Step{Seq: len(st.History) + 1, State: cur, Kind: "ask", Role: "oracle", Task: oracleTask, StartedAt: time.Now()}
	art, artDir, err := startArtifact(r.RunDir, step.Seq, cur)
	if err != nil {
		step.Key, step.Error = "error", err.Error()
		return r.escalate(st, step, "The oracle could not start: "+err.Error())
	}
	step.Artifact = art
	// Save the counter before the oracle starts, as the run loop does for agents.
	st.AgentSeq++
	if r.Store != nil {
		if err := r.Store.Save(st); err != nil {
			return err
		}
	}
	var stats StepStats
	rc := RunContext{
		AgentSeq:      st.AgentSeq,
		Stats:         &stats,
		Repo:          st.Repo,
		Branch:        st.Branch,
		Worktree:      st.Worktree,
		RunDir:        r.RunDir,
		DefaultBranch: r.DefaultBranch,
		Reason:        MaskSecrets(st.Ask.Reason),
		Run:           st.Run,
		StateName:     cur,
		Workflow:      st.Workflow,
		Pause:         MaskSecrets(st.Ask.Message),
		Goto:          gotoStates(r.WF),
		Issue:         st.Issue,
		PR:            st.PR,
		ArtifactDir:   artDir,
		RunSoFar:      runSoFar(st, cur, r.RunDir),
	}
	answer, session, runErr := r.Agents.Run(ctx, "oracle", oracleTask, rc)
	sealArtifact(artDir)
	step.EndedAt = time.Now()
	step.Session = session
	step.Stats = agentStats(&stats)
	if ctx.Err() != nil {
		// No answer. The pause stays for a human.
		return r.savePaused(st)
	}
	if runErr != nil {
		step.Key, step.Error = "error", runErr.Error()
		return r.escalate(st, step, "The oracle failed: "+runErr.Error())
	}
	ans, why := parseAskAnswer(answer)
	step.Key = MaskSecrets(ans)
	step.Note = MaskSecrets(why)
	if word, _, _ := strings.Cut(ans, " "); word == "ask" {
		return r.escalate(st, step, "The oracle asks a human: "+why)
	}
	next, note, ok := oracleTarget(r.WF, s, ans)
	if !ok {
		return r.escalate(st, step, fmt.Sprintf("The oracle answer is not valid: %q", ans))
	}
	if word, _, _ := strings.Cut(ans, " "); word == "stop" && st.PR > 0 && !WasMerged(st) {
		return r.escalate(st, step, "The oracle proposes stop, a human must confirm: "+why)
	}
	if note != "" {
		st.Note = MaskSecrets(note)
	}
	step.To = next
	r.warn("workflow run " + st.Run + ": oracle answered " + step.Key + ": " + step.Note)
	return r.moveOn(ctx, st, step, next, false)
}

// escalate records the oracle step and leaves the run paused for a human. The
// reason is added to the pause message.
func (r *Runner) escalate(st *RunState, step Step, reason string) error {
	reason = MaskSecrets(reason)
	step.To = st.Current
	step.Note = reason
	st.History = append(st.History, step)
	st.Ask.Message += " " + reason
	return r.savePaused(st)
}

// parseAskAnswer splits an oracle answer into the answer (the first non-empty line)
// and the reason (the other lines). Backticks and asterisks around a line are
// removed. Without a reason, the reason is "no reason given".
func parseAskAnswer(text string) (answer, reason string) {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.Trim(strings.TrimSpace(l), "`*"); strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	switch len(lines) {
	case 0:
		return "", "no reason given"
	case 1:
		return lines[0], "no reason given"
	}
	return lines[0], strings.Join(lines[1:], " ")
}

// oracleTarget returns the state that the oracle answer ans selects in the ask
// state s, as a human answer does (see Resume). The answers are "retry",
// "retry <note>", "goto <state>" and "stop". ok is false for any other answer and
// for a target that the workflow does not allow. note is the text after retry.
func oracleTarget(wf *Workflow, s State, ans string) (next, note string, ok bool) {
	word, rest, _ := strings.Cut(ans, " ")
	rest = strings.TrimSpace(rest)
	switch {
	case word == "goto" && rest != "":
		return rest, "", checkGoto(wf, rest) == nil
	case word == "retry":
		next, ok = onTarget(s, "retry")
		return next, rest, ok
	case word == "stop" && rest == "":
		next, ok = onTarget(s, "stop")
		return next, "", ok
	}
	return "", "", false
}

// onTarget returns the target of key in the on map of s, else the target of "*".
func onTarget(s State, key string) (string, bool) {
	if next, ok := s.On[key]; ok {
		return next, true
	}
	next, ok := s.On["*"]
	return next, ok
}

// gotoStates lists the states that an oracle "goto" may name, for the task text.
func gotoStates(wf *Workflow) string {
	var names []string
	for name := range wf.States {
		if checkGoto(wf, name) == nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
