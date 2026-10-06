package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrPaused is returned when the run enters an ask state.
// Pause/resume logic beyond this stub belongs to the max_visits issue.
var ErrPaused = errors.New("run paused")

// Run walks the workflow until it reaches end (status done), ask
// (status paused, ErrPaused), or a failure (status failed).
//
// The caller creates st.Worktree before the first state (SDR 5.3); Run never
// creates worktrees. An empty st.Current starts at WF.Start.
//
// Transition rules: the check or agent key picks the next state from on;
// "default" is the fallback; no entry and no default fails the run with
// Reason unknown transition key "<k>" in state "<s>".
// An agent error routes through the "error" key (then "default"); with
// neither route the run fails. A check-runner error fails the run directly.
// Context cancellation fails the run with Reason "cancelled".
// When the run reaches end with no agent step ever recorded, OnSkip is
// called if set. A panic in the loop is recovered into a failed run.
func (r *Runner) Run(ctx context.Context, st *RunState) (err error) {
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
			if !ranAgent(st) && r.OnSkip != nil {
				r.OnSkip(st)
			}
			r.notify("run " + st.Run + " done")
			return nil
		}
		if s.Ask != "" {
			st.Status = "paused"
			st.Ask = &Ask{Message: s.Ask}
			st.UpdatedAt = time.Now()
			if r.Store != nil {
				if saveErr := r.Store.Save(st); saveErr != nil {
					return saveErr
				}
			}
			r.notify("run " + st.Run + " paused: " + s.Ask)
			return ErrPaused
		}

		st.Visits[cur]++
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
			started := time.Now()
			env := buildCheckEnv(st, s, r.RunDir)
			key, res, runErr := r.Checks.Run(ctx, s, env, st.Worktree)
			ended := time.Now()
			if ctx.Err() != nil {
				return r.fail(ctx, st, "cancelled", ctx.Err())
			}
			if runErr != nil {
				return r.fail(ctx, st, runErr.Error(), runErr)
			}
			next, ok := route(s, key)
			if !ok {
				return r.failUnknownKey(st, cur, key)
			}
			readPRFile(st, r.RunDir)
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
			started := time.Now()
			rc := RunContext{
				Repo:      st.Repo,
				Branch:    st.Branch,
				Worktree:  st.Worktree,
				RunDir:    r.RunDir,
				Reason:    MaskSecrets(st.Reason),
				StateName: cur,
				Issue:     st.Issue,
				PR:        st.PR,
				Visit:     st.Visits[cur],
			}
			key, session, runErr := r.Agents.Run(ctx, s.Agent, s.Task, rc)
			ended := time.Now()
			if ctx.Err() != nil {
				return r.fail(ctx, st, "cancelled", ctx.Err())
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
			next, ok := route(s, key)
			if !ok {
				return r.failUnknownKey(st, cur, key)
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
				Session:   session,
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

func (r *Runner) failUnknownKey(st *RunState, cur, key string) error {
	reason := fmt.Sprintf("unknown transition key %q in state %q", key, cur)
	st.History = append(st.History, Step{
		Seq:       len(st.History) + 1,
		State:     cur,
		Kind:      kindOf(r.WF.States[cur]),
		Key:       key,
		To:        "",
		StartedAt: time.Now(),
		EndedAt:   time.Now(),
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
