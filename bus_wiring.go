package main

import (
	"fmt"
	"io"
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

// busLog receives the lines that report a failed publish. A failed publish
// must not fail the job that made it, so the error is only written here.
var busLog io.Writer = os.Stderr

// busOrchestratorNotices is the orchestrator's subscription to completion
// notices. wireTools creates it on appBus.
var busOrchestratorNotices *bus.Sub

// agentInboxes holds the inbox of every live job. wireTools creates it.
var agentInboxes *inboxSet

// orchestratorAddr is the address of the main conversation.
var orchestratorAddr = bus.Addr{Type: bus.AddrOrchestrator}

// noticeKinds lists the kinds that an inbox or the orchestrator reads. A
// subscription gets only the kinds it lists, so every consumer lists all of
// them.
var noticeKinds = []bus.Kind{bus.KindNoticeCompletion, bus.KindAskRequest}

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
		Kinds: noticeKinds,
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
			fmt.Fprintf(busLog, "bus: notice for %s not forwarded: %v\n", id, err)
		}
	}
}

// publishTo sends payload as kind from from to to. A publish error is logged
// and dropped, because a failing bus must not fail the job that sent it.
func publishTo[T any](b *bus.Bus, kind bus.Kind, from, to bus.Addr, origin bus.Origin, payload T) {
	if _, err := bus.Publish(b, kind, from, to, origin, payload); err != nil {
		fmt.Fprintf(busLog, "bus: %s not published: %v\n", kind, err)
	}
}

// agentAddr returns the address of agent id.
func agentAddr(id string) bus.Addr {
	return bus.Addr{Type: bus.AddrAgent, ID: id}
}

// publishNotice sends one completion notice to parentID, or to the
// orchestrator when parentID is "".
func publishNotice(b *bus.Bus, parentID, text string) {
	to := orchestratorAddr
	if parentID != "" {
		to = agentAddr(parentID)
	}
	publishTo(b, bus.KindNoticeCompletion, orchestratorAddr, to, bus.OriginSystem,
		bus.Completion{Text: text})
}

// publishAsk sends the question of agent agentID to parentID, or to the
// orchestrator when parentID is "". The bus reroutes it to the orchestrator
// when parentID has finished.
func publishAsk(b *bus.Bus, parentID, agentID string, seq int, text string) {
	to := orchestratorAddr
	if parentID != "" {
		to = agentAddr(parentID)
	}
	publishTo(b, bus.KindAskRequest, agentAddr(agentID), to, bus.OriginAgent,
		bus.AskRequest{Agent: agentID, QuestionSeq: seq, Text: text})
}

// maxShownAsks bounds the asks that a handoff message already carried. When
// it is full, an arbitrary mark is dropped first.
const maxShownAsks = 64

// askKey identifies one question of one agent.
type askKey struct {
	agent string
	seq   int
}

var (
	shownAsksMu sync.Mutex
	shownAsks   = make(map[askKey]bool)
)

// markAskShown records that a handoff message carried question seq of agent.
// The ask is left out of the next drain that reaches it, whether it was
// published before or after this call.
func markAskShown(agent string, seq int) {
	shownAsksMu.Lock()
	defer shownAsksMu.Unlock()
	if len(shownAsks) >= maxShownAsks {
		for k := range shownAsks {
			delete(shownAsks, k)
			break
		}
	}
	shownAsks[askKey{agent, seq}] = true
}

// takeShownAsk reports whether the ask was marked, and clears the mark.
func takeShownAsk(agent string, seq int) bool {
	shownAsksMu.Lock()
	defer shownAsksMu.Unlock()
	k := askKey{agent, seq}
	if !shownAsks[k] {
		return false
	}
	delete(shownAsks, k)
	return true
}

// formatNotices returns the text of each message that the model should read.
// A message that the bus rerouted from a finished agent says so, and names
// the agent.
func formatNotices(msgs []bus.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		text, ok := noticeText(m)
		if !ok {
			continue
		}
		if m.OrigTo != nil {
			text = fmt.Sprintf("[for agent %s, which has already finished — forwarded here instead] %s", m.OrigTo.ID, text)
		}
		out = append(out, text)
	}
	return out
}

// noticeText returns the text of m. ok is false when m must not be shown: it
// is an ask that a handoff message already carried.
func noticeText(m bus.Message) (text string, ok bool) {
	switch m.Kind {
	case bus.KindNoticeCompletion:
		c, err := bus.Decode[bus.Completion](m)
		if err != nil {
			fmt.Fprintf(busLog, "bus: notice %d not read: %v\n", m.Seq, err)
			return "", false
		}
		return c.Text, true
	case bus.KindAskRequest:
		a, err := bus.Decode[bus.AskRequest](m)
		if err != nil {
			fmt.Fprintf(busLog, "bus: ask %d not read: %v\n", m.Seq, err)
			return "", false
		}
		if takeShownAsk(a.Agent, a.QuestionSeq) {
			return "", false
		}
		return a.Text, true
	}
	fmt.Fprintf(busLog, "bus: message %d of kind %q not shown\n", m.Seq, m.Kind)
	return "", false
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

// MarkQuestionShown records a handoff message's question on the bus path. The
// ask itself is published by the bus, so the mark is kept here.
func (n noticeCounter) MarkQuestionShown(jobID string, seq int) {
	markAskShown(jobID, seq)
}

// watchdogNotify sends a watchdog alarm to the agent to, or to the human when
// to is "". It reports false when to is not live, so that the watchdog climbs
// to the next ancestor. The bus would otherwise reroute the alarm to the
// orchestrator.
func watchdogNotify(to, text string) bool {
	if to == "" {
		publishNotice(appBus, "", text)
		return true
	}
	if !JobRegistry.IsLive(to) {
		return false
	}
	publishNotice(appBus, to, text)
	return true
}
