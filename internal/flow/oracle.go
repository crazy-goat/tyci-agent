package flow

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// defaultOracleAnswers is the oracle answer limit of a workflow without
// defaults.oracle_answers.
const defaultOracleAnswers = 2

// oracleTask is the task template of the oracle answer to an ask pause.
const oracleTask = "ask"

// isPauseOracle reports whether the agent call is the oracle answer to an ask
// pause. The answer is the text of the agent, so the oracle writes no report.md.
func isPauseOracle(role, task string) bool {
	return role == "oracle" && task == oracleTask
}

// legacySavedTasks are the oracle tasks that save their answer in a workflow without
// a "saves" flag: the pick of the findings of a milestone-close run (#206), the split
// of a rebalance run (#208) and the plan of a risky issue (#214).
var legacySavedTasks = []string{"pick_critical", "rebalance_split", "plan"}

// savesAnswer reports whether the agent state name saves the oracle answer as
// output.txt for the next state (#784). Such an oracle writes no report.md. A state
// with "saves": true saves. When no state of the workflow has the flag, a state
// with the oracle agent and a legacy task name saves, as in a workflow that predates
// the flag.
func savesAnswer(wf *Workflow, name string) bool {
	s := wf.States[name]
	if s.Saves {
		return true
	}
	for _, other := range wf.States {
		if other.Saves {
			return false
		}
	}
	return s.Agent == "oracle" && slices.Contains(legacySavedTasks, s.Task)
}

// oracleTaskProblem returns the problem of the workflow in dir when the oracle
// needs tasks/ask.md and the file is missing. The oracle answers the pauses of
// each ask state without "human": true, unless defaults.oracle_answers is 0.
// It returns "" when there is no problem.
func oracleTaskProblem(wf *Workflow, dir string) string {
	if n := wf.Defaults.OracleAnswers; n != nil && *n == 0 {
		return ""
	}
	var asks []string
	for _, name := range sortedStates(wf) {
		if s := wf.States[name]; s.Ask != "" && !s.Human {
			asks = append(asks, fmt.Sprintf("%q", name))
		}
	}
	if len(asks) == 0 {
		return ""
	}
	if _, err := flowconfig.ReadPromptFile(dir, taskFile(oracleTask)); err == nil {
		return ""
	}
	return fmt.Sprintf("%s is missing: the oracle needs it to answer the pauses of the ask states %s. "+
		"Add the file, set \"human\": true on those states, or set defaults.oracle_answers to 0.",
		taskFile(oracleTask), strings.Join(asks, ", "))
}

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
// answer, or an oracle error, leaves the run paused for a human. The key line of
// the answer selects the route, the other lines are the reason (see parseOracleAnswer).
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
		Goto:          gotoStates(r.WF, st),
		Issue:         st.Issue,
		Params:        st.Params,
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
	key, note, reason, ok := parseOracleAnswer(answer)
	if !ok {
		line := oracleFirstLine(answer)
		step.Key = MaskSecrets(line)
		return r.escalate(st, step, fmt.Sprintf("The oracle answer is not valid: %q", line))
	}
	step.Key = MaskSecrets(strings.TrimSpace(key + " " + note))
	why := reason
	if (key == "ask" || key == "stop") && note != "" {
		why = strings.TrimSpace(note + " " + reason)
	}
	if why == "" {
		why = "no reason given"
	}
	step.Note = MaskSecrets(why)
	if key == "ask" {
		return r.escalate(st, step, "The oracle asks a human: "+why)
	}
	next, outNote, ok := oracleTarget(r.WF, s, key, note)
	if !ok {
		return r.escalate(st, step, fmt.Sprintf("The oracle answer is not valid: %q", strings.TrimSpace(key+" "+note)))
	}
	if next == "merge" {
		return r.escalate(st, step, "The oracle proposes a merge, a human must confirm: "+why)
	}
	if isGuarded(r.WF, next) {
		return r.escalate(st, step, "The oracle proposes the state "+next+", a human must confirm: "+why)
	}
	if oracleBlocked(r.WF, st, key, next) {
		if key == "stop" {
			return r.escalate(st, step, "The oracle proposes stop, a human must confirm: "+why)
		}
		return r.escalate(st, step, "The oracle proposes the state "+next+" while the PR is open, a human must confirm: "+why)
	}
	if outNote != "" {
		st.Note = MaskSecrets(outNote)
	}
	step.To = next
	r.warn("workflow run " + ShowRunID(st.Run) + ": oracle answered " + step.Key + ": " + step.Note)
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

// parseOracleAnswer reads an oracle answer and finds its key line: the first line
// whose first word is a key. The keys are "goto:<state>" (or "goto <state>"),
// "retry", "stop" and "ask". Key is the key in one form: "goto:<state>", "retry",
// "stop" or "ask". Note is the text after the key on that line. Reason is the text
// of the other lines, joined with spaces. ok is false when no line has a key.
func parseOracleAnswer(text string) (key, note, reason string, ok bool) {
	var others []string
	for _, line := range oracleLines(text) {
		if k, n, valid := parseKeyLine(line); valid && !ok {
			key, note, ok = k, n, true
			continue
		}
		others = append(others, line)
	}
	return key, note, strings.Join(others, " "), ok
}

// parseKeyLine reads one line of an oracle answer as a key line (see
// parseOracleAnswer). A trailing ".,;:!?" after the key is ignored.
func parseKeyLine(line string) (key, note string, ok bool) {
	word, rest, _ := strings.Cut(line, " ")
	word = strings.ToLower(strings.TrimRight(word, ".,;:!?"))
	rest = strings.TrimSpace(rest)
	switch {
	case word == "stop" || word == "retry" || word == "ask":
		return word, rest, true
	case strings.HasPrefix(word, "goto:") && len(word) > len("goto:"):
		return word, rest, true
	case word == "goto" && rest != "":
		state, after, _ := strings.Cut(rest, " ")
		return "goto:" + state, strings.TrimSpace(after), true
	}
	return "", "", false
}

// oracleLines returns the non-empty lines of an oracle answer. Spaces, backticks
// and asterisks around a line are removed.
func oracleLines(text string) []string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "`*")); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// oracleFirstLine returns the first non-empty line of an oracle answer, or "".
func oracleFirstLine(text string) string {
	if lines := oracleLines(text); len(lines) > 0 {
		return lines[0]
	}
	return ""
}

// oracleTarget returns the state that the oracle key selects in the ask state s,
// as a human answer does (see Resume). The keys are "goto:<state>", "retry" and
// "stop". ok is false for any other key and for a target that the workflow does
// not allow. outNote is the note for the next worker prompt.
func oracleTarget(wf *Workflow, s State, key, note string) (next, outNote string, ok bool) {
	switch {
	case strings.HasPrefix(key, "goto:"):
		target := strings.TrimPrefix(key, "goto:")
		return target, note, checkGoto(wf, target) == nil
	case key == "retry":
		next, ok = onTarget(s, "retry")
		return next, note, ok
	case key == "stop":
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

// oracleBlocked reports whether an oracle answer with the key, which sends the run
// to the state name, must go to a human. A guarded state always does (see
// isGuarded). While the PR is open, a stop does too, and so does a state that ends
// the run or a state of the oracle agent, which can stop the run.
func oracleBlocked(wf *Workflow, st *RunState, key, name string) bool {
	if isGuarded(wf, name) {
		return true
	}
	if st.PR == 0 || WasMerged(st) {
		return false
	}
	return key == "stop" || wf.States[name].Agent == "oracle" || leavesRun(wf, name)
}

// isGuarded reports whether the state name needs a human before an oracle may send
// the run there (#562). A state with "guard": true is guarded. When no state of the
// workflow has the flag, the state named "merge" is guarded, as in a workflow that
// predates the flag.
func isGuarded(wf *Workflow, name string) bool {
	if wf.States[name].Guard {
		return true
	}
	for _, s := range wf.States {
		if s.Guard {
			return false
		}
	}
	return name == "merge"
}

// leavesRun reports whether the state name ends the run, or whether every path
// from it does. A state without targets counts as ending the run. A guarded state
// and "$failed" count as the run going on. A loop back to a state of the walk does
// not end the run.
func leavesRun(wf *Workflow, name string) bool {
	return endsRun(wf, name, map[string]bool{})
}

// endsRun is leavesRun with the result of each state. A state that is still on
// the walk is stored as false, so a loop does not end the run.
func endsRun(wf *Workflow, name string, result map[string]bool) bool {
	if done, ok := result[name]; ok {
		return done
	}
	s, ok := wf.States[name]
	if !ok || isGuarded(wf, name) {
		return false
	}
	if s.End {
		return true
	}
	result[name] = false
	ends := true
	for _, next := range s.On {
		if next == FailedTarget || !endsRun(wf, next, result) {
			ends = false
			break
		}
	}
	result[name] = ends
	return ends
}

// gotoStates lists the states that an oracle "goto" may name, for the task text.
// It leaves out the states that oracleBlocked sends to a human.
func gotoStates(wf *Workflow, st *RunState) string {
	var names []string
	for name := range wf.States {
		if checkGoto(wf, name) == nil && !oracleBlocked(wf, st, "goto:"+name, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
