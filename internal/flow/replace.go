package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/runlog"
)

// replaceWait is how long a replacement waits for the old agent to stop. A
// variable so that tests do not wait 10 s.
var replaceWait = 10 * time.Second

// agentSlot tracks the agent that runs in one workflow run, so that the
// orchestrator can replace it (replace_agent, #202). The runner sets it before
// each agent and clears it after. The Manager calls replace from the chat goroutine.
type agentSlot struct {
	mu  sync.Mutex
	run string
	// validate checks that a role is defined. Nil: every role is accepted.
	validate func(role string) error
	// role, cancel and done describe the running agent. cancel is nil when no
	// agent runs. done is closed when the running agent returns.
	role   string
	visit  string // state and visit number, for example "code@2"
	cancel context.CancelFunc
	done   chan struct{}
	// used holds the visits that already had a replacement (1 per visit).
	used map[string]bool
	// pending is the replacement that the runner takes when the agent returns.
	pending *pendingReplacement
}

// pendingReplacement is a replacement that waits for the old agent to return.
type pendingReplacement struct {
	toRole string
	reason string
}

func newAgentSlot(run string, validate func(string) error) *agentSlot {
	return &agentSlot{run: run, validate: validate, used: map[string]bool{}}
}

// begin marks an agent of role as running in visit. cancel stops it.
func (s *agentSlot) begin(role, visit string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.role, s.visit, s.cancel = role, visit, cancel
	s.done = make(chan struct{})
}

// end marks the running agent as returned. It closes done and returns the
// pending replacement, or nil.
func (s *agentSlot) end() *pendingReplacement {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.pending
	s.pending = nil
	s.cancel = nil
	close(s.done)
	return p
}

// replace stops the running agent and sets the replacement. agentID must be the
// id of the running agent, <run>/<role>. The new role is checked before the old
// agent is touched. The call waits at most replaceWait for the old agent to
// return; if it does not, the replacement is refused.
func (s *agentSlot) replace(agentID, toRole, reason string) error {
	s.mu.Lock()
	if s.cancel == nil {
		s.mu.Unlock()
		return fmt.Errorf("run %s has no running agent", s.run)
	}
	if want := s.run + "/" + s.role; agentID != want {
		s.mu.Unlock()
		return fmt.Errorf("agent %q is not the running agent; the running agent is %q", agentID, want)
	}
	if s.validate != nil {
		if err := s.validate(toRole); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	if s.used[s.visit] {
		s.mu.Unlock()
		return fmt.Errorf("the agent of this state visit (%s) was already replaced; one replacement per visit", s.visit)
	}
	s.used[s.visit] = true
	p := &pendingReplacement{toRole: toRole, reason: reason}
	s.pending = p
	cancel, done := s.cancel, s.done
	s.mu.Unlock()

	cancel()
	timer := time.NewTimer(replaceWait)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == p {
		// The old agent did not return in time: refuse, and let it end as it would.
		s.pending = nil
		delete(s.used, s.visit)
		return fmt.Errorf("agent %s did not stop within %s; the replacement is refused", agentID, replaceWait)
	}
	// The old agent returned, and the runner took the replacement in the meantime.
	return nil
}

// runAgent runs the agent of state cur. When the orchestrator replaces the agent,
// the old agent is stopped, the summary is built and the new role runs in the
// same visit. Visits are not counted again. It returns the role that ran last.
func (r *Runner) runAgent(ctx context.Context, st *RunState, cur string, s State, rc RunContext) (string, string, string, error) {
	if r.slot == nil {
		key, session, err := r.Agents.Run(ctx, s.Agent, s.Task, rc)
		return s.Agent, key, session, err
	}
	role := s.Agent
	for {
		actx, cancel := context.WithCancel(ctx)
		r.slot.begin(role, cur+"@"+strconv.Itoa(rc.Visit), cancel)
		key, session, err := r.Agents.Run(actx, role, s.Task, rc)
		cancel()
		p := r.slot.end()
		if p == nil {
			return role, key, session, err
		}
		next, sumErr := r.replaceWith(st, cur, rc, role, p)
		if sumErr != nil {
			r.warn("summary of the replaced agent: " + sumErr.Error())
		}
		rc = next
		role = p.toRole
	}
}

// replaceWith builds the summary of the stopped agent, records the replacement
// in the run state and returns the run context of the new agent.
func (r *Runner) replaceWith(st *RunState, cur string, rc RunContext, from string, p *pendingReplacement) (RunContext, error) {
	summary, err := BuildSummary(rc.Worktree, StoppedAgent{
		Role:          from,
		Reason:        p.reason,
		DefaultBranch: r.DefaultBranch,
		Progress:      transcriptLines(rc.RunDir, from, rc.AgentSeq),
	})
	now := time.Now()
	st.Replaced = append(st.Replaced, Replacement{
		State: cur, Visit: rc.Visit, FromAgent: st.Run + "/" + from,
		ToRole: p.toRole, Reason: p.reason, At: now,
	})
	st.AgentSeq++
	st.UpdatedAt = now
	if r.Store != nil {
		if saveErr := r.Store.Save(st); saveErr != nil && err == nil {
			err = saveErr
		}
	}
	if r.Notify != nil {
		r.Notify(fmt.Sprintf("run %s: replaced %s with %s in state %s: %s", st.Run, from, p.toRole, cur, p.reason))
	}
	rc.AgentSeq = st.AgentSeq
	rc.Summary = summary
	return rc, err
}

// transcriptLines returns the lines of the transcript of an agent, or nil when
// the run has no transcript.
func transcriptLines(runDir, role string, seq int) []string {
	if runDir == "" || seq <= 0 {
		return nil
	}
	b, err := os.ReadFile(runlog.Path(runDir, role, seq))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// Replace implements tools.WorkflowManager for replace_agent. agentID is
// <run>/<role> of the running agent. It returns once the old agent has stopped,
// or with an error when the replacement is refused. The new agent starts after that.
func (m *Manager) Replace(agentID, role, reason string) error {
	run, _, ok := strings.Cut(agentID, "/")
	if !ok || run == "" {
		return fmt.Errorf("agent_id %q must be <run>/<role>", agentID)
	}
	m.mu.Lock()
	a, active := m.active[run]
	m.mu.Unlock()
	if !active || a.slot == nil {
		return fmt.Errorf("run %q is not active", run)
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("reason is required")
	}
	return a.slot.replace(agentID, role, strings.TrimSpace(reason))
}
