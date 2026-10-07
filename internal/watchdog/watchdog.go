// Package watchdog notices running subagents that show no activity and tells
// their parent. It only notifies: it never cancels, restarts or nudges a job.
package watchdog

import (
	"context"
	"fmt"
	"time"

	"github.com/crazy-goat/tyci-agent/jobs"
)

const (
	// DefaultIdleAfter and DefaultEscalateAfter are used when config sets nothing.
	DefaultIdleAfter     = 3 * time.Minute
	DefaultEscalateAfter = 3 * time.Minute

	// TickInterval is how often Run calls Tick.
	TickInterval = 15 * time.Second

	maxDepth  = 16
	humanSent = -1
)

// Watchdog walks the registry on every Tick and escalates idle jobs one level
// up the parent chain at a time, ending at the human.
type Watchdog struct {
	Reg           *jobs.Registry
	IdleAfter     time.Duration
	EscalateAfter time.Duration
	// Notify delivers text to a job's mailbox; toJobID "" means the human.
	// It returns false when the job cannot receive the message.
	Notify func(toJobID, text string) bool

	// state is touched only by the ticker goroutine (and tests calling Tick).
	state map[string]*escState
}

type escState struct {
	level  int // 1 parent, 2 grandparent, ...; humanSent once the human was told
	sentAt time.Time
}

// Run calls Tick every TickInterval until ctx is done.
func (w *Watchdog) Run(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			w.Tick(now)
		}
	}
}

// Tick checks every running subagent against now.
func (w *Watchdog) Tick(now time.Time) {
	if w.state == nil {
		w.state = map[string]*escState{}
	}
	list := w.Reg.List()
	byID := make(map[string]jobs.Job, len(list))
	for _, j := range list {
		byID[j.ID] = j
	}
	for _, j := range list {
		if j.Kind != jobs.KindSubagent || j.Status != jobs.StatusRunning {
			// Waiting-for-answer is the parent's slowness, not the job's.
			delete(w.state, j.ID)
			continue
		}
		last := j.LastActivity
		if last.IsZero() || last.UnixNano() <= 0 {
			last = j.StartedAt
		}
		idle := now.Sub(last)
		st := w.state[j.ID]
		switch {
		case idle < w.IdleAfter:
			delete(w.state, j.ID)
		case st == nil:
			w.escalate(j, byID, 1, idle, now)
		case st.level != humanSent && now.Sub(st.sentAt) >= w.EscalateAfter:
			w.escalate(j, byID, st.level+1, idle, now)
		}
	}
	// Forget jobs that left the registry.
	for id := range w.state {
		if _, ok := byID[id]; !ok {
			delete(w.state, id)
		}
	}
}

// escalate notifies the ancestor `level` steps above j. A missing or
// unreachable ancestor moves to the next level in the same call. Past the top
// (or past maxDepth, which guards against cycles) the human is told.
func (w *Watchdog) escalate(j jobs.Job, byID map[string]jobs.Job, level int, idle time.Duration, now time.Time) {
	text := message(j, idle)
	for ; level <= maxDepth; level++ {
		anc, ok := ancestor(j, byID, level)
		if !ok || anc == "" {
			break
		}
		if w.Notify(anc, text) {
			w.state[j.ID] = &escState{level: level, sentAt: now}
			return
		}
	}
	w.Notify("", text)
	w.state[j.ID] = &escState{level: humanSent, sentAt: now}
}

// ancestor returns the ID of the job `level` parent links above j. It returns
// "" when the chain reaches the top-level conversation (no parent) and
// ok=false when a link points to an unknown job.
func ancestor(j jobs.Job, byID map[string]jobs.Job, level int) (string, bool) {
	id := j.ParentID
	for i := 1; i < level; i++ {
		if id == "" {
			return "", true
		}
		p, ok := byID[id]
		if !ok {
			return "", false
		}
		id = p.ParentID
	}
	return id, true
}

func message(j jobs.Job, idle time.Duration) string {
	note := j.Progress
	if note == "" {
		note = "none"
	}
	return fmt.Sprintf("[watchdog] Agent %s (%s) has shown no activity for %s.\n"+
		"Last progress note: %q.\n"+
		"You may: send it a message (message), or cancel it (kill_job).",
		j.ID, j.Description, formatIdle(idle), note)
}

// formatIdle shows whole seconds below one minute and rounded minutes above.
func formatIdle(idle time.Duration) string {
	if idle < time.Minute {
		return idle.Round(time.Second).String()
	}
	return fmt.Sprintf("%dm", int((idle+30*time.Second)/time.Minute))
}
