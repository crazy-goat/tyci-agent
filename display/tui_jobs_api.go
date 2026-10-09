package display

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/jobs"
)

// SetJobEvents feeds the jobs panel from sub, a subscription to the job.status
// messages of the bus (see publishStatus in main). The TUI does not poll
// jobs.Registry itself. A nil sub is a no-op: a caller that never wires a
// subscription gets no jobs panel, ever.
//
// The bus keeps only the newest job.status message per job, so a busy TUI
// skips intermediate updates but always gets each job's latest state.
//
// The subscriber goroutine exits when t.done closes (program shutdown) or
// when sub is closed or its bus is closed, whichever comes first; it never
// blocks program exit.
func (t *TUI) SetJobEvents(sub *bus.Sub) {
	if sub == nil {
		return
	}
	go func() {
		defer sub.Close()
		forwardJobUpdates(sub, t.done, t.prog.Send)
	}()
}

// forwardJobUpdates sends the job snapshot of each drained job.status message
// to send until done closes or the subscription ends. Messages still pending
// when the subscription ends are delivered first.
func forwardJobUpdates(sub *bus.Sub, done <-chan struct{}, send func(tea.Msg)) {
	deliver := func() {
		for _, m := range sub.Drain() {
			st, err := bus.Decode[bus.JobStatus](m)
			if err != nil {
				continue
			}
			send(tuiMsgJobUpdate{Job: jobFromStatus(st, m.Seq)})
		}
	}
	for {
		select {
		case <-sub.Ready():
			deliver()
		case <-sub.Done():
			deliver()
			return
		case <-done:
			return
		}
	}
}

// jobFromStatus rebuilds the job snapshot that the jobs panel reads from a
// job.status payload. seq is the bus Seq of the message. It orders the
// snapshots of one job, so it becomes the job's EventSeq.
func jobFromStatus(st bus.JobStatus, seq uint64) jobs.Job {
	return jobs.Job{
		ID:           st.ID,
		ParentID:     st.ParentID,
		Kind:         jobs.Kind(st.Kind),
		Description:  st.Name,
		Status:       jobs.Status(st.Status),
		Progress:     st.Progress,
		Result:       st.Result,
		Err:          st.Err,
		Question:     st.Question,
		StartedAt:    st.Started,
		FinishedAt:   st.Ended,
		LastActivity: st.LastActivity,
		EventSeq:     seq,
	}
}
