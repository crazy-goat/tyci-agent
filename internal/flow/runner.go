package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrPaused is returned when the run enters an ask state.
var ErrPaused = errors.New("flow: run paused")

// FailedTarget is the on target "the last check step": the fixer returns "ok"
// and the step that failed runs again (#369).
const FailedTarget = "$failed"

// recoveryCaps limits the recovery roles per failed check step (#369): the
// fixer runs at most twice for the same failed step, the oracle once. Over the
// cap the state is skipped as if the agent had answered key.
var recoveryCaps = map[string]struct {
	max int
	key string
}{"fixer": {2, "failed"}, "oracle": {1, "ask"}}

// Run walks the workflow until it reaches end (status done), ask
// (status paused, ErrPaused), or a failure (status failed).
//
// The caller creates st.Worktree before the first state (SDR 5.3); Run never
// creates worktrees. An empty st.Current starts at WF.Start.
//
// Transition rules: the check or agent key picks the next state from on;
// "default" is the fallback; no entry and no default fails the run with
// Reason unknown transition key "<k>" in state "<s>".
// An agent answer is "<key> [note]": the note goes to Step.Note, and for a
// "goto:<state>" key also to the next worker prompt. "goto:<state>" continues at
// that state unless on maps the key itself. The target FailedTarget is the last
// check step. The fixer and oracle roles are capped per failed step (recoveryCaps).
// An agent error routes through the "error" key (then "default"); with
// neither route the run fails. A check-runner error fails the run directly.
// Context cancellation fails the run with Reason "cancelled".
// When the run reaches end with no agent step ever recorded, or after a
// merge state returned "merged", OnSkip is called if set.
// A panic in the loop is recovered into a failed run.
func (r *Runner) Run(ctx context.Context, st *RunState) error {
	return r.run(ctx, st, false)
}

// Continue runs st again from its saved state after a restart. The saved state
// is entered without a new visit, so a crash loop does not use up max_visits.
func (r *Runner) Continue(ctx context.Context, st *RunState) error {
	return r.run(ctx, st, true)
}

// run is Run; again skips the visit count of the first state.
func (r *Runner) run(ctx context.Context, st *RunState, again bool) (err error) {
	defer func() {
		if p := recover(); p != nil {
			st.Status = "failed"
			st.Reason = fmt.Sprintf("panic in state %q: %v", st.Current, p)
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				_ = r.Store.Save(st)
			}
			r.notify("run " + st.Run + " failed: " + st.Reason)
			err = fmt.Errorf("%s", st.Reason)
		}
	}()

	if st == nil {
		return errors.New("flow: run state is nil")
	}
	if r.WF == nil {
		return errors.New("flow: workflow is nil")
	}
	if st.Current == "" {
		st.Current = r.WF.Start
	}
	if st.Visits == nil {
		st.Visits = map[string]int{}
	}
	if st.StartedAt.IsZero() {
		st.StartedAt = time.Now()
	}
	st.Status = "running"
	st.PID = os.Getpid()
	st.UpdatedAt = time.Now()

	for {
		if ctx.Err() != nil {
			return r.fail(ctx, st, "cancelled", ctx.Err())
		}
		cur := st.Current
		s, ok := r.WF.States[cur]
		if !ok {
			return r.fail(ctx, st, fmt.Sprintf("unknown state %q", cur), fmt.Errorf("unknown state %q", cur))
		}
		if s.End {
			st.Status = "done"
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				if saveErr := r.Store.Save(st); saveErr != nil {
					return saveErr
				}
			}
			if note := doneProposalNote(st, r.RunDir); note != "" {
				r.warn(note)
			}
			if (!ranAgent(st) || wasMerged(st)) && r.OnSkip != nil {
				r.OnSkip(st)
			}
			r.notify("run " + st.Run + " done")
			return nil
		}
		if s.Ask != "" {
			return r.pause(st, s.Ask, "")
		}
		newVisit := !again
		if again {
			again = false
		} else {
			if limit := r.effectiveLimit(s); limit > 0 && st.Visits[cur]+1 > limit {
				askState, ok := r.WF.States["ask"]
				if !ok || askState.Ask == "" {
					return r.fail(ctx, st, `max_visits needs an "ask" state`, errors.New(`max_visits needs an "ask" state`))
				}
				st.Current = "ask"
				return r.pause(st, askState.Ask, "max_visits:"+cur)
			}
			st.Visits[cur]++
		}
		if r.Store != nil {
			if saveErr := r.Store.Save(st); saveErr != nil {
				return saveErr
			}
		}

		switch {
		case s.Check != "":
			if r.Checks == nil {
				return r.fail(ctx, st, fmt.Sprintf("no check runner for state %q", cur), fmt.Errorf("no check runner for state %q", cur))
			}
			art, artDir, artErr := startArtifact(r.RunDir, len(st.History)+1, cur)
			if artErr != nil {
				return r.fail(ctx, st, artErr.Error(), artErr)
			}
			started := time.Now()
			env := append(buildCheckEnv(st, s, r.RunDir, r.DefaultBranch), "TYCI_ARTIFACT_DIR="+artDir)
			key, res, runErr := r.Checks.Run(ctx, s, env, st.Worktree)
			ended := time.Now()
			if artDir != "" {
				_ = os.WriteFile(filepath.Join(artDir, "output.log"), []byte(res.Output), 0o600)
				sealArtifact(artDir)
			}
			if ctx.Err() != nil {
				return r.fail(ctx, st, "cancelled", ctx.Err())
			}
			if runErr != nil {
				return r.fail(ctx, st, runErr.Error(), runErr)
			}
			next, ok := route(s, key)
			if !ok {
				return r.failUnknownKey(st, cur, key, art)
			}
			readPRFile(st, r.RunDir)
			readLastCommentID(st, r.RunDir)
			var warnings []string
			if key == "fail" && filepath.Base(s.Check) == "post_review.sh" {
				warnings = []string{"review_post_failed"}
				r.warn("run " + st.Run + ": posting the review to the PR failed")
			}
			st.History = append(st.History, Step{
				Seq:        len(st.History) + 1,
				State:      cur,
				Kind:       "check",
				Key:        key,
				To:         next,
				StartedAt:  started,
				EndedAt:    ended,
				Exit:       res.Exit,
				StderrTail: res.StderrTail,
				Warnings:   warnings,
				Artifact:   art,
			})
			st.Current = next
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				if saveErr := r.Store.Save(st); saveErr != nil {
					return saveErr
				}
			}
		case s.Agent != "":
			if r.Agents == nil {
				return r.fail(ctx, st, fmt.Sprintf("no agent runner for state %q", cur), fmt.Errorf("no agent runner for state %q", cur))
			}
			failed := lastCheck(st)
			if c, capped := recoveryCaps[s.Agent]; capped && newVisit {
				capKey := cur + "@" + failed.State
				if st.Visits[capKey] >= c.max {
					if err := r.skipCapped(st, s, cur, c.key, fmt.Sprintf("%s already ran %d time(s) for the failed step %s", s.Agent, c.max, failed.State)); err != nil {
						return err
					}
					continue
				}
				st.Visits[capKey]++
			}
			art, artDir, artErr := startArtifact(r.RunDir, len(st.History)+1, cur)
			if artErr != nil {
				return r.fail(ctx, st, artErr.Error(), artErr)
			}
			started := time.Now()
			// Save the counter before the agent starts: after a crash the resume
			// must get the next number, never reuse a transcript file.
			st.AgentSeq++
			if r.Store != nil {
				if saveErr := r.Store.Save(st); saveErr != nil {
					return saveErr
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
				Reason:        MaskSecrets(st.Reason),
				Run:           st.Run,
				Note:          st.Note,
				StateName:     cur,
				Workflow:      st.Workflow,
				Prompt:        s.Prompt,
				Issue:         st.Issue,
				PR:            st.PR,
				Visit:         st.Visits[cur],
				ArtifactDir:   artDir,
				RunSoFar:      runSoFar(st, cur, r.RunDir),
			}
			if _, capped := recoveryCaps[s.Agent]; capped {
				rc.Failed, rc.FailedKey = failed.State, failed.Key
				if failed.Artifact != "" && r.RunDir != "" {
					rc.FailedDir = filepath.Join(r.RunDir, "artifacts", failed.Artifact)
				}
			}
			key, session, runErr := r.Agents.Run(ctx, s.Agent, s.Task, rc)
			ended := time.Now()
			sealArtifact(artDir)
			if ctx.Err() != nil {
				return r.fail(ctx, st, "cancelled", ctx.Err())
			}
			if s.Agent == "worker" {
				st.Note = ""
			}
			if errors.Is(runErr, ErrNoArtifact) {
				return r.pauseNoArtifact(ctx, st, cur, s.Agent, art, started, ended, session, runErr)
			}
			if runErr != nil {
				key = "error"
				next, ok := route(s, key)
				if !ok {
					reason := fmt.Sprintf("agent failed in state %q: %v", cur, runErr)
					st.History = append(st.History, Step{
						Seq:       len(st.History) + 1,
						State:     cur,
						Kind:      "agent",
						Key:       key,
						To:        "",
						StartedAt: started,
						EndedAt:   ended,
						Role:      s.Agent,
						Stats:     agentStats(&stats),
						Artifact:  art,
					})
					return r.fail(ctx, st, reason, runErr)
				}
				st.History = append(st.History, Step{
					Seq:       len(st.History) + 1,
					State:     cur,
					Kind:      "agent",
					Key:       key,
					To:        next,
					StartedAt: started,
					EndedAt:   ended,
					Role:      s.Agent,
					Stats:     agentStats(&stats),
					Error:     runErr.Error(),
					Artifact:  art,
				})
				st.Current = next
				st.UpdatedAt = time.Now()
				if r.Store != nil {
					if saveErr := r.Store.Save(st); saveErr != nil {
						return saveErr
					}
				}
				continue
			}
			key, note, _ := strings.Cut(strings.TrimSpace(key), " ")
			note = MaskSecrets(strings.TrimSpace(note))
			next, ok := r.agentRoute(st, s, key)
			if !ok {
				return r.failUnknownKey(st, cur, key, art)
			}
			if strings.HasPrefix(key, "goto:") && note != "" {
				st.Note = note
			}
			st.History = append(st.History, Step{
				Seq:       len(st.History) + 1,
				State:     cur,
				Kind:      "agent",
				Key:       key,
				To:        next,
				StartedAt: started,
				EndedAt:   ended,
				Role:      s.Agent,
				Stats:     agentStats(&stats),
				Session:   session,
				Artifact:  art,
				Note:      note,
			})
			st.Current = next
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				if saveErr := r.Store.Save(st); saveErr != nil {
					return saveErr
				}
			}
		default:
			return r.fail(ctx, st,
				fmt.Sprintf("state %q has no check, agent, ask or end", cur),
				fmt.Errorf("state %q has no check, agent, ask or end", cur))
		}
	}
}

// agentRoute is route for an agent key, plus "goto:<state>" and FailedTarget.
func (r *Runner) agentRoute(st *RunState, s State, key string) (string, bool) {
	next, ok := route(s, key)
	if target, isGoto := strings.CutPrefix(key, "goto:"); isGoto {
		if _, mapped := s.On[key]; !mapped && checkGoto(r.WF, target) == nil {
			next, ok = target, true
		}
	}
	if ok && next == FailedTarget {
		next = lastCheck(st).State
		ok = next != ""
	}
	return next, ok
}

// lastCheck returns the last check step of the run (zero Step when none).
func lastCheck(st *RunState) Step {
	for i := len(st.History) - 1; i >= 0; i-- {
		if st.History[i].Kind == "check" {
			return st.History[i]
		}
	}
	return Step{}
}

// skipCapped records a recovery state that is over its cap and moves on as if
// the agent had answered key. note says why; a pause shows it.
func (r *Runner) skipCapped(st *RunState, s State, cur, key, note string) error {
	next, ok := r.agentRoute(st, s, key)
	if !ok {
		return r.failUnknownKey(st, cur, key, "")
	}
	now := time.Now()
	st.History = append(st.History, Step{
		Seq: len(st.History) + 1, State: cur, Kind: "agent", Key: key, To: next,
		StartedAt: now, EndedAt: now, Role: s.Agent, Note: note,
	})
	st.Current = next
	st.UpdatedAt = now
	if r.Store != nil {
		return r.Store.Save(st)
	}
	return nil
}

// pauseNoArtifact records the agent step and pauses the run in the "ask"
// state with the reason "no artifact from <role>".
func (r *Runner) pauseNoArtifact(ctx context.Context, st *RunState, cur, role, art string, started, ended time.Time, session string, runErr error) error {
	askState, ok := r.WF.States["ask"]
	if !ok || askState.Ask == "" {
		return r.fail(ctx, st, runErr.Error(), runErr)
	}
	st.History = append(st.History, Step{
		Seq:       len(st.History) + 1,
		State:     cur,
		Kind:      "agent",
		Key:       "error",
		To:        "ask",
		StartedAt: started,
		EndedAt:   ended,
		Role:      role,
		Session:   session,
		Error:     runErr.Error(),
		Artifact:  art,
	})
	st.Current = "ask"
	return r.pause(st, askState.Ask, runErr.Error())
}

// readPRFile copies the PR number from <runDir>/pr (written by push.sh).
func readPRFile(st *RunState, runDir string) {
	if runDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(runDir, "pr"))
	if err != nil {
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && n > 0 {
		st.PR = n
	}
}

// readLastCommentID copies the comment ids from <runDir>/last_comment_id and
// <runDir>/last_review_comment_id (written by fetch_comments.sh). The ids only grow.
func readLastCommentID(st *RunState, runDir string) {
	if runDir == "" {
		return
	}
	readID := func(name string, dst *int64) {
		b, err := os.ReadFile(filepath.Join(runDir, name))
		if err != nil {
			return
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && n > *dst {
			*dst = n
		}
	}
	readID("last_comment_id", &st.LastCommentID)
	readID("last_review_comment_id", &st.LastReviewCommentID)
}

func route(s State, key string) (string, bool) {
	if next, ok := s.On[key]; ok {
		return next, true
	}
	if next, ok := s.On["default"]; ok {
		return next, true
	}
	return "", false
}

func (r *Runner) fail(_ context.Context, st *RunState, reason string, err error) error {
	st.Status = "failed"
	st.Reason = reason
	st.UpdatedAt = time.Now()
	if r.Store != nil {
		_ = r.Store.Save(st)
	}
	r.notify("run " + st.Run + " failed: " + reason)
	if err == nil {
		return errors.New(reason)
	}
	return err
}

func (r *Runner) failUnknownKey(st *RunState, cur, key, art string) error {
	reason := fmt.Sprintf("unknown transition key %q in state %q", key, cur)
	st.History = append(st.History, Step{
		Seq:       len(st.History) + 1,
		State:     cur,
		Kind:      kindOf(r.WF.States[cur]),
		Key:       key,
		To:        "",
		StartedAt: time.Now(),
		EndedAt:   time.Now(),
		Artifact:  art,
	})
	st.Status = "failed"
	st.Reason = reason
	st.UpdatedAt = time.Now()
	if r.Store != nil {
		_ = r.Store.Save(st)
	}
	r.notify("run " + st.Run + " failed: " + reason)
	return errors.New(reason)
}

func kindOf(s State) string {
	if s.Check != "" {
		return "check"
	}
	if s.Agent != "" {
		return "agent"
	}
	return "check"
}

// wasMerged reports whether the merge check returned "merged".
func wasMerged(st *RunState) bool {
	for _, h := range st.History {
		if h.Kind == "check" && h.State == "merge" && h.Key == "merged" {
			return true
		}
	}
	return false
}

func ranAgent(st *RunState) bool {
	for _, h := range st.History {
		if h.Kind == "agent" {
			return true
		}
	}
	return false
}

func (r *Runner) notify(msg string) {
	if r.Notify != nil {
		r.Notify(msg)
	}
}

// effectiveLimit returns the visit limit of a state: its own max_visits,
// else defaults.max_visits. 0 means unlimited.
func (r *Runner) effectiveLimit(s State) int {
	if s.MaxVisits != 0 {
		return s.MaxVisits
	}
	return r.WF.Defaults.MaxVisits
}

// pause saves the run as paused and returns ErrPaused.
func (r *Runner) pause(st *RunState, message, reason string) error {
	st.Status = "paused"
	if n := len(st.History); n > 0 && st.History[n-1].Error != "" {
		h := st.History[n-1]
		message += fmt.Sprintf(" (%s failed: %s)", h.State, h.Error)
	} else if n > 0 && st.History[n-1].Note != "" {
		h := st.History[n-1]
		message += fmt.Sprintf(" (%s: %s)", h.State, h.Note)
		if reason == "" {
			reason = h.Note
		}
	} else if n > 0 {
		h := st.History[n-1]
		message += fmt.Sprintf(" (last step: %s, key %s", h.State, h.Key)
		if line := MaskSecrets(lastLine(h.StderrTail)); line != "" {
			message += ": " + line
		}
		message += ")"
	}
	st.Ask = &Ask{Message: message, Reason: reason}
	if dir := findProposal(st, r.RunDir); dir != "" {
		st.Ask.Proposal = dir
		st.Ask.Message += proposalWaits + proposalSummary(dir) +
			" (workflow_status shows it; answer apply or reject)"
	}
	st.UpdatedAt = time.Now()
	if r.Store != nil {
		if err := r.Store.Save(st); err != nil {
			return err
		}
	}
	r.notify("run " + st.Run + " paused: " + st.Ask.Message)
	return ErrPaused
}

// checkGoto rejects a goto target that is not a state of wf, is an ask state
// or is an end state (a run ends only through its flow steps).
func checkGoto(wf *Workflow, state string) error {
	if s, known := wf.States[state]; !known || s.Ask != "" || s.End {
		return fmt.Errorf("flow: unknown goto state %q", state)
	}
	return nil
}

// Resume answers a paused run. The answer only selects a key of the ask
// state's on map (then "*"); it never runs anything. Two forms add to this:
// "retry <note>" keeps the retry route and stores the note for the next worker
// prompt, and "goto <state>" continues at that state. An unknown answer keeps
// the run paused and returns an error that lists the allowed keys.
// Leaving the ask state for a state that is not an end state resets all
// visit counters. An ask state without on ends the run done.
func (r *Runner) Resume(ctx context.Context, st *RunState, answer string) error {
	if st == nil || r.WF == nil {
		return errors.New("flow: run state or workflow is nil")
	}
	if st.Status != "paused" {
		return fmt.Errorf("flow: run is %q, not paused", st.Status)
	}
	s, ok := r.WF.States[st.Current]
	if !ok || s.Ask == "" {
		return fmt.Errorf("flow: current state %q is not an ask state", st.Current)
	}
	word, rest, _ := strings.Cut(strings.TrimSpace(answer), " ")
	rest = strings.TrimSpace(rest)
	var next string
	switch {
	case word == "goto" && rest != "":
		if err := checkGoto(r.WF, rest); err != nil {
			return err
		}
		next, ok = rest, true
	case word == "retry" && rest != "":
		next, ok = s.On[word]
		if !ok {
			next, ok = s.On["*"]
		}
		if ok {
			st.Note = MaskSecrets(rest)
		}
	default:
		next, ok = s.On[answer]
	}
	if !ok {
		next, ok = s.On["*"]
	}
	if !ok && len(s.On) > 0 {
		keys := make([]string, 0, len(s.On))
		for k := range s.On {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return fmt.Errorf("flow: unknown answer %q, allowed: %s", answer, strings.Join(keys, ", "))
	}
	now := time.Now()
	st.History = append(st.History, Step{
		Seq:       len(st.History) + 1,
		State:     st.Current,
		Kind:      "ask",
		Key:       answer,
		To:        next,
		StartedAt: now,
		EndedAt:   now,
	})
	st.Ask = nil
	st.UpdatedAt = now
	if !ok {
		st.Status = "done"
		if r.Store != nil {
			if err := r.Store.Save(st); err != nil {
				return err
			}
		}
		r.notify("run " + st.Run + " done")
		return nil
	}
	if !r.WF.States[next].End {
		st.Visits = map[string]int{}
	}
	st.Current = next
	st.Status = "running"
	st.PID = os.Getpid()
	if r.Store != nil {
		if err := r.Store.Save(st); err != nil {
			return err
		}
	}
	return r.Run(ctx, st)
}

func (r *Runner) warn(msg string) {
	if r.Warn != nil {
		r.Warn(msg)
	}
}

// agentStats returns s for the history, or nil when the agent reported no usage.
func agentStats(s *StepStats) *StepStats {
	if *s == (StepStats{}) {
		return nil
	}
	c := *s
	return &c
}
