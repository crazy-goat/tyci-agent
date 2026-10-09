package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/internal/redact"
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
var noticeKinds = []bus.Kind{bus.KindNoticeCompletion, bus.KindAskRequest, bus.KindBtwAnswer, bus.KindAgentMessage}

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

// recipientAddr returns the address of the spawner id, or of the orchestrator
// when id is "".
func recipientAddr(id string) bus.Addr {
	if id == "" {
		return orchestratorAddr
	}
	return agentAddr(id)
}

// publishNotice sends one completion notice to parentID, or to the
// orchestrator when parentID is "". A quiet notice does not wake an idle chat.
func publishNotice(b *bus.Bus, parentID, text string, quiet bool) {
	publishTo(b, bus.KindNoticeCompletion, orchestratorAddr, recipientAddr(parentID), bus.OriginSystem,
		bus.Completion{Text: text, Quiet: quiet})
}

// publishAsk sends the question of agent agentID to parentID, or to the
// orchestrator when parentID is "". The bus reroutes it to the orchestrator
// when parentID has finished.
func publishAsk(b *bus.Bus, parentID, agentID string, seq int, text string) {
	publishTo(b, bus.KindAskRequest, agentAddr(agentID), recipientAddr(parentID), bus.OriginAgent,
		bus.AskRequest{Agent: agentID, QuestionSeq: seq, Text: text})
}

// publishBtwAnswer sends the answer of the /btw evaluation jobID to parentID,
// or to the orchestrator when parentID is "". The bus reroutes it when
// parentID has finished.
func publishBtwAnswer(b *bus.Bus, parentID, jobID, question, answer string) {
	publishTo(b, bus.KindBtwAnswer, agentAddr(jobID), recipientAddr(parentID), bus.OriginAgent,
		bus.BtwAnswer{Question: question, Text: answer, JobID: jobID})
}

// publishAgentMessage sends text from one sender to agent id. origin says what
// caused it: the "message" tool and the /msg command set it.
func publishAgentMessage(b *bus.Bus, id string, from bus.Addr, origin bus.Origin, text string) {
	publishTo(b, bus.KindAgentMessage, from, agentAddr(id), origin, bus.AgentMessage{Text: text})
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
	case bus.KindAgentMessage:
		a, err := bus.Decode[bus.AgentMessage](m)
		if err != nil {
			fmt.Fprintf(busLog, "bus: message %d not read: %v\n", m.Seq, err)
			return "", false
		}
		return a.Text, true
	case bus.KindBtwAnswer:
		a, err := bus.Decode[bus.BtwAnswer](m)
		if err != nil {
			fmt.Fprintf(busLog, "bus: btw answer %d not read: %v\n", m.Seq, err)
			return "", false
		}
		return btwEvaluationNotice(a.Question, a.JobID, a.Text), true
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

// pendingNotices holds the notices of the main conversation that were read from
// the bus but not handed out yet. pendingLoud is true when one of them may wake
// an idle chat.
var (
	pendingMu      sync.Mutex
	pendingNotices []string
	pendingLoud    bool
)

// quietNotice reports whether m is a quiet completion notice.
func quietNotice(m bus.Message) bool {
	if m.Kind != bus.KindNoticeCompletion {
		return false
	}
	c, err := bus.Decode[bus.Completion](m)
	return err == nil && c.Quiet
}

// stashBusNotices moves the waiting notices of the orchestrator's subscription
// to pendingNotices, so that they are kept in order for the next drain.
func stashBusNotices() {
	if busOrchestratorNotices == nil {
		return
	}
	var fresh []string
	loud := false
	for _, m := range busOrchestratorNotices.Drain() {
		fresh = append(fresh, formatNotices([]bus.Message{m})...)
		loud = loud || !quietNotice(m)
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingNotices = append(pendingNotices, fresh...)
	pendingLoud = pendingLoud || loud
}

// drainNotices returns every waiting notice of the main conversation, quiet
// ones included.
func drainNotices() []string {
	stashBusNotices()
	pendingMu.Lock()
	defer pendingMu.Unlock()
	out := pendingNotices
	pendingNotices = nil
	pendingLoud = false
	return out
}

// wakeNotices is drainNotices for the TUI's wake-up. It returns nothing when
// only quiet notices arrived, and keeps them for the next drain.
func wakeNotices() []string {
	stashBusNotices()
	pendingMu.Lock()
	loud := pendingLoud
	pendingMu.Unlock()
	if !loud {
		return nil
	}
	return drainNotices()
}

// clearNotices drops every waiting notice of the main conversation. /new uses
// it so that no notice of the old conversation reaches the new one.
func clearNotices() {
	if busOrchestratorNotices != nil {
		busOrchestratorNotices.Drain()
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	pendingNotices = nil
	pendingLoud = false
}

// noticeCounter counts the notices of the main conversation, so that wait sees
// a notice that arrived. It satisfies tools.JobNotifier.
type noticeCounter struct{}

func (noticeCounter) Queued() uint64 {
	if busOrchestratorNotices == nil {
		return 0
	}
	return busOrchestratorNotices.Accepted()
}

// MarkAskShown records a handoff message's question. See markAskShown.
func (noticeCounter) MarkAskShown(jobID string, seq int) {
	markAskShown(jobID, seq)
}

// watchdogNotify sends a watchdog alarm to the agent to, or to the human when
// to is "". It reports false when to is not live, so that the watchdog climbs
// to the next ancestor. The bus would otherwise reroute the alarm to the
// orchestrator.
func watchdogNotify(to, text string) bool {
	if to == "" {
		publishNotice(appBus, "", text, false)
		return true
	}
	if !JobRegistry.IsLive(to) {
		return false
	}
	publishNotice(appBus, to, text, false)
	return true
}
