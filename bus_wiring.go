package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/jobs"
	"github.com/crazy-goat/tyci-agent/session"
)

// busJournalPath returns the journal file for the working directory, or ""
// when its session directory does not exist yet.
func busJournalPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir, err := session.SessionDir(wd)
	if err != nil {
		return ""
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return filepath.Join(dir, "bus.jsonl")
}

// newAppBus returns a bus that routes to JobRegistry's agent tree. It journals
// to journalPath when that is not empty, with every journal line passed
// through redact.Redact first.
func newAppBus(journalPath string) *bus.Bus {
	return bus.New(
		bus.WithTree(JobRegistry.BusTree()),
		bus.WithJournal(journalPath),
		bus.WithRedactor(redactJournalLine),
	)
}

// redactJournalLine applies the secret redaction of internal/redact to one
// journal line.
func redactJournalLine(line []byte) []byte {
	return []byte(redact.Redact(string(line)))
}

// appBus is the process-wide message bus. main replaces it with the journal
// bus before wireTools, and tests replace it with a fresh bus.
var appBus = newAppBus("")

// busOrchestratorNotices is the orchestrator's subscription to completion
// notices. wireTools creates it on appBus.
var busOrchestratorNotices *bus.Sub

// agentInboxes holds the inbox of every live job. wireTools creates it.
var agentInboxes *inboxSet

// orchestratorAddr is the address of the main conversation.
var orchestratorAddr = bus.Addr{Type: bus.AddrOrchestrator}

// inboxSet keeps one Durable inbox subscription per agent. The inbox of an
// agent opens when the job starts, before its ID is returned, and closes when
// the job is terminal. Messages that the inbox still holds then go to the
// orchestrator.
type inboxSet struct {
	b    *bus.Bus
	mu   sync.Mutex
	subs map[string]*bus.Sub
}

func newInboxSet(b *bus.Bus) *inboxSet {
	return &inboxSet{b: b, subs: make(map[string]*bus.Sub)}
}

// open subscribes the inbox of agent id. A second call for the same agent
// does nothing, so an agent never has two inboxes.
func (s *inboxSet) open(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.subs[id]; ok {
		return
	}
	s.subs[id] = s.b.Subscribe("inbox "+id, bus.Filter{
		To:    bus.Addr{Type: bus.AddrAgent, ID: id},
		Kinds: []bus.Kind{bus.KindNoticeCompletion},
	})
}

// drain returns the notices that wait in the inbox of agent id.
func (s *inboxSet) drain(id string) []string {
	s.mu.Lock()
	sub := s.subs[id]
	s.mu.Unlock()
	if sub == nil {
		return nil
	}
	return formatNotices(sub.Drain())
}

// accepted counts the notices that reached the inbox of agent id.
func (s *inboxSet) accepted(id string) uint64 {
	s.mu.Lock()
	sub := s.subs[id]
	s.mu.Unlock()
	if sub == nil {
		return 0
	}
	return sub.Accepted()
}

// close ends the inbox of agent id. Its waiting messages go to the
// orchestrator, so that none is lost. Closing an unknown agent does nothing.
func (s *inboxSet) close(id string) {
	s.mu.Lock()
	sub := s.subs[id]
	delete(s.subs, id)
	s.mu.Unlock()
	if sub == nil {
		return
	}
	for _, m := range sub.CloseAndDrain() {
		if _, err := s.b.Forward(m, orchestratorAddr); err != nil {
			fmt.Fprintf(os.Stderr, "bus: notice for %s not forwarded: %v\n", id, err)
		}
	}
}

// publishNotice sends one completion notice to parentID, or to the
// orchestrator when parentID is "". A publish error is logged, because a
// failing bus must not fail the job that produced the notice.
func publishNotice(b *bus.Bus, parentID, text string) {
	to := orchestratorAddr
	if parentID != "" {
		to = bus.Addr{Type: bus.AddrAgent, ID: parentID}
	}
	if _, err := bus.Publish(b, bus.KindNoticeCompletion, orchestratorAddr, to,
		bus.OriginSystem, bus.Completion{Text: text}); err != nil {
		fmt.Fprintf(os.Stderr, "bus: notice not published: %v\n", err)
	}
}

// formatNotices returns the text of each notice. A notice that the bus
// rerouted from a finished agent says so, and names the agent.
func formatNotices(msgs []bus.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		c, err := bus.Decode[bus.Completion](m)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bus: notice %d not read: %v\n", m.Seq, err)
			continue
		}
		text := c.Text
		if m.OrigTo != nil {
			text = fmt.Sprintf("[for agent %s, which has already finished — forwarded here instead] %s", m.OrigTo.ID, text)
		}
		out = append(out, text)
	}
	return out
}

// drainNotices returns the waiting notices of the main conversation: the
// queue of JobNotices first, then the bus notices.
func drainNotices() []string {
	out := JobNotices.Drain()
	if busOrchestratorNotices == nil {
		return out
	}
	return append(out, formatNotices(busOrchestratorNotices.Drain())...)
}

// noticeCounter counts the notices of the main conversation on both paths, so
// that wait sees a notice that reached either one.
type noticeCounter struct{ *jobs.Notifier }

func (n noticeCounter) Queued() uint64 {
	if busOrchestratorNotices == nil {
		return n.Notifier.Queued()
	}
	return n.Notifier.Queued() + busOrchestratorNotices.Accepted()
}
