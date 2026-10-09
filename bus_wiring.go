package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/bus"
	"github.com/crazy-goat/tyci-agent/internal/redact"
	"github.com/crazy-goat/tyci-agent/internal/watchdog"
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
	return filepath.Join(dir, session.JournalFileName)
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
var noticeKinds = []bus.Kind{bus.KindNoticeCompletion, bus.KindAskRequest, bus.KindBtwAnswer, bus.KindAgentMessage, bus.KindPingMissed}

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

// publishBtwSuggestion sends the answer of a busy-line fork jobID to the TUI,
// which shows it in the chat at once, and to the orchestrator, which reads it
// at its next drain. Both copies are published once each.
func publishBtwSuggestion(b *bus.Bus, jobID, question, answer string) {
	payload := bus.BtwAnswer{Question: question, Text: answer, JobID: jobID, Suggestion: true}
	publishTo(b, bus.KindBtwAnswer, agentAddr(jobID), orchestratorAddr, bus.OriginAgent, payload)
	publishTo(b, bus.KindBtwAnswer, agentAddr(jobID), bus.Addr{Type: bus.AddrTUI}, bus.OriginAgent, payload)
}

// publishAgentMessage sends text from one sender to agent id. origin says what
// caused it: the "message" tool and the /msg command set it.
func publishAgentMessage(b *bus.Bus, id string, from bus.Addr, origin bus.Origin, text string) {
	publishTo(b, bus.KindAgentMessage, from, agentAddr(id), origin, bus.AgentMessage{Text: text})
}

// maxShownAsks bounds the asks that a handoff message already carried. When
// it is full, an arbitrary mark is dropped first.
//
// A mark is removed when a drain reaches its ask. A mark for an ask that a
// drain already showed is never removed that way, so it stays until the
// eviction. The eviction can then drop a live mark, and that can show one
// question twice. The bound keeps the map small, and a duplicate is the only
// effect. A question is never lost.
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
		out = append(out, tagSender(m, text))
	}
	return out
}

// tagSender puts the sender of m in front of text. Only a message from a
// person (OriginHuman) is a plain user turn. Every other origin, including a
// message from an agent, gets the tag, so the model never reads agent text as
// something a person wrote.
func tagSender(m bus.Message, text string) string {
	if m.Origin == bus.OriginHuman {
		return text
	}
	return fmt.Sprintf("[notice from=%s kind=%s] %s", addrLabel(m.From), m.Kind, text)
}

// addrLabel renders an address as type or type:id, for the tag.
func addrLabel(a bus.Addr) string {
	if a.ID == "" {
		return string(a.Type)
	}
	return string(a.Type) + ":" + a.ID
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
	case bus.KindPingMissed:
		p, err := bus.Decode[bus.PingMissed](m)
		if err != nil {
			fmt.Fprintf(busLog, "bus: alarm %d not read: %v\n", m.Seq, err)
			return "", false
		}
		note := p.LastNote
		if note == "" {
			note = "none"
		}
		return fmt.Sprintf("[watchdog] Agent %s (%s) has shown no activity for %s.\n"+
			"Last progress note: %q.\n"+
			"You may: send it a message (message), or cancel it (kill_job).",
			p.Agent, p.Description, formatIdle(p.QuietFor), note), true
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
		if a.Suggestion {
			return btwSuggestionNotice(a.Question, a.JobID, a.Text), true
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

// quietNotice reports whether m waits for the next drain instead of waking an
// idle chat: a quiet completion notice, or a btw suggestion.
func quietNotice(m bus.Message) bool {
	switch m.Kind {
	case bus.KindNoticeCompletion:
		c, err := bus.Decode[bus.Completion](m)
		return err == nil && c.Quiet
	case bus.KindBtwAnswer:
		a, err := bus.Decode[bus.BtwAnswer](m)
		return err == nil && a.Suggestion
	}
	return false
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

// inboxEvent opens or closes the inbox of a job from one of its snapshots.
// Only the start snapshot (EventSeq 1) opens the inbox. Snapshots can reach
// the hook out of order (#131), so a Running snapshot that comes after the
// terminal one must not open an inbox that nothing closes again.
func inboxEvent(inboxes *inboxSet, j jobs.Job) {
	switch {
	case j.Status == jobs.StatusRunning && j.EventSeq == 1:
		inboxes.open(j.ID)
	case j.Status != jobs.StatusRunning && j.Status != jobs.StatusWaitingAnswer:
		inboxes.close(j.ID)
	}
}

// withOrchestratorNotices adds the orchestrator's notices to a NextMessages
// drain. The modes without a TUI (tyci run, which cron also runs) use it, so
// that the model reads its notices at the next safe point.
func withOrchestratorNotices(base func() []string) func() []string {
	return mergeNextMessages(base, drainNotices)
}

// dropLeftoverNotices drops the notices that no model turn read, and writes
// their count to w. The modes that end without a model turn after the
// notices arrive (the workflow CLI, the end of tyci run) call it at the end.
func dropLeftoverNotices(w io.Writer) {
	if n := len(drainNotices()); n > 0 {
		fmt.Fprintf(w, "Note: %d background notice(s) arrived too late to be shown and were dropped.\n", n)
	}
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
func watchdogNotify(to string, a watchdog.Alarm) bool {
	if to == "" {
		publishPingMissed(appBus, "", a)
		return true
	}
	if !JobRegistry.IsLive(to) {
		return false
	}
	publishPingMissed(appBus, to, a)
	return true
}

// publishPingMissed sends a watchdog alarm about agent a.Agent to parentID, or
// to the orchestrator when parentID is "".
func publishPingMissed(b *bus.Bus, parentID string, a watchdog.Alarm) {
	publishTo(b, bus.KindPingMissed, bus.Addr{Type: bus.AddrAgent, ID: a.Agent}, recipientAddr(parentID), bus.OriginSystem,
		bus.PingMissed{Agent: a.Agent, Description: a.Description, LastNote: a.LastNote, QuietFor: a.QuietFor})
}

// formatIdle shows whole seconds below one minute and rounded minutes above.
func formatIdle(idle time.Duration) string {
	if idle < time.Minute {
		return idle.Round(time.Second).String()
	}
	return fmt.Sprintf("%dm", int((idle+30*time.Second)/time.Minute))
}

// jobStatusOf converts a job snapshot to the payload of job.status.
func jobStatusOf(j jobs.Job) bus.JobStatus {
	return bus.JobStatus{
		ID:           j.ID,
		ParentID:     j.ParentID,
		Kind:         string(j.Kind),
		Name:         j.Description,
		Status:       string(j.Status),
		Progress:     j.Progress,
		Result:       j.Result,
		Err:          j.Err,
		Question:     j.Question,
		Started:      j.StartedAt,
		Ended:        j.FinishedAt,
		LastActivity: j.LastActivity,
	}
}

// subscribeJobStatus subscribes to the job.status messages that the TUI reads.
func subscribeJobStatus(b *bus.Bus) *bus.Sub {
	return b.Subscribe("tui-jobs", bus.Filter{To: bus.Addr{Type: bus.AddrTUI}, Kinds: []bus.Kind{bus.KindJobStatus}})
}

// subscribeBtwSuggestions subscribes to the btw answers that the TUI shows in
// the chat.
func subscribeBtwSuggestions(b *bus.Bus) *bus.Sub {
	return b.Subscribe("tui-btw", bus.Filter{To: bus.Addr{Type: bus.AddrTUI}, Kinds: []bus.Kind{bus.KindBtwAnswer}})
}

// showBtwSuggestions passes the text of each drained btw suggestion to show,
// until done closes or the subscription ends.
func showBtwSuggestions(sub *bus.Sub, done <-chan struct{}, show func(string)) {
	defer sub.Close()
	deliver := func() {
		for _, m := range sub.Drain() {
			a, err := bus.Decode[bus.BtwAnswer](m)
			if err != nil || !a.Suggestion {
				continue
			}
			show(btwSuggestionNotice(a.Question, a.JobID, a.Text))
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
