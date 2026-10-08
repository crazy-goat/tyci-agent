package tools

import (
	"sync"

	"github.com/crazy-goat/tyci-agent/stream"
)

// liveTranscriptCap is how many job transcripts stay in memory. The oldest
// one is dropped first, so a long session does not grow without limit.
const liveTranscriptCap = 100

// LiveEvent is one step of a subagent's conversation, in the order the
// subagent produced it. Kind is "thinking", "text", "tool-start", "tool-delta"
// or "tool-end". These are the same names the TUI uses for its own block
// messages, so the display replays events without a mapping table.
type LiveEvent struct {
	Kind     string
	Content  string
	ToolName string
}

// liveTranscript holds the events of one job. The job's streamingCollector
// writes it while the job runs, and the TUI's agent view reads it.
type liveTranscript struct {
	mu     sync.Mutex
	events []LiveEvent
}

var liveTranscripts = struct {
	sync.Mutex
	byJob map[string]*liveTranscript
	order []string
}{byJob: make(map[string]*liveTranscript)}

// startLiveTranscript returns the transcript of jobID, creating it on the
// first call. Later calls with the same jobID return the same transcript.
func startLiveTranscript(jobID string) *liveTranscript {
	liveTranscripts.Lock()
	defer liveTranscripts.Unlock()
	if lt, ok := liveTranscripts.byJob[jobID]; ok {
		return lt
	}
	lt := &liveTranscript{}
	liveTranscripts.byJob[jobID] = lt
	liveTranscripts.order = append(liveTranscripts.order, jobID)
	for len(liveTranscripts.order) > liveTranscriptCap {
		delete(liveTranscripts.byJob, liveTranscripts.order[0])
		liveTranscripts.order = liveTranscripts.order[1:]
	}
	return lt
}

// add appends ev. A nil transcript (a child with no job id) records nothing.
func (lt *liveTranscript) add(ev LiveEvent) {
	if lt == nil {
		return
	}
	lt.mu.Lock()
	lt.events = append(lt.events, ev)
	lt.mu.Unlock()
}

// addAll appends evs in order.
func (lt *liveTranscript) addAll(evs []LiveEvent) {
	lt.mu.Lock()
	lt.events = append(lt.events, evs...)
	lt.mu.Unlock()
}

// RecordLiveEvent appends ev to the transcript of jobID and creates the
// transcript on first use. A subagent records its events through its own
// collector. This entry point lets other packages feed a transcript as well,
// for example the TUI tests of the agent view.
func RecordLiveEvent(jobID string, ev LiveEvent) {
	startLiveTranscript(jobID).add(ev)
}

// CopyLiveTranscript appends the events that fromJobID recorded so far to the
// transcript of toJobID. A resumed or promoted job calls it with the job it
// continues, so its agent view keeps the turns before the new run. When
// fromJobID has no transcript (dropped or never started), nothing is copied.
func CopyLiveTranscript(fromJobID, toJobID string) {
	events, _ := LiveTranscriptSince(fromJobID, 0)
	startLiveTranscript(toJobID).addAll(events)
}

// EventSink is the method set of agent.Sink and ledger.Sink. Any sink of an
// agent run satisfies it, so NewLiveSink can wrap it.
type EventSink interface {
	Request(content string)
	Thinking(text string)
	Text(text string)
	ToolCallStart(name string)
	ToolCallDelta(delta string)
	ToolCallEnd(name string, result string)
	ToolFinish()
	ToolBlock(msg string)
	Summary(usage stream.Usage, stats stream.Stats)
	Total(usage stream.Usage)
	Error(err error)
	End()
}

// liveSink forwards every call to the wrapped sink. It also records the text,
// thinking and tool events in the live transcript, the same events that
// streamingCollector records for a child run by runSingleTask.
type liveSink struct {
	EventSink
	live *liveTranscript
}

// NewLiveSink wraps next so the conversation of jobID also goes to its live
// transcript. Use it for a job that does not run through runSingleTask: a
// resumed job, a promoted /btw job or a /btw evaluation.
func NewLiveSink(jobID string, next EventSink) EventSink {
	return liveSink{EventSink: next, live: startLiveTranscript(jobID)}
}

func (s liveSink) Thinking(text string) {
	s.EventSink.Thinking(text)
	s.live.add(LiveEvent{Kind: "thinking", Content: text})
}

func (s liveSink) Text(text string) {
	s.EventSink.Text(text)
	s.live.add(LiveEvent{Kind: "text", Content: text})
}

func (s liveSink) ToolCallStart(name string) {
	s.EventSink.ToolCallStart(name)
	s.live.add(LiveEvent{Kind: "tool-start", ToolName: name})
}

func (s liveSink) ToolCallDelta(delta string) {
	s.EventSink.ToolCallDelta(delta)
	s.live.add(LiveEvent{Kind: "tool-delta", Content: delta})
}

func (s liveSink) ToolCallEnd(name, result string) {
	s.EventSink.ToolCallEnd(name, result)
	s.live.add(LiveEvent{Kind: "tool-end", Content: result})
}

// HasLiveTranscript reports whether a transcript exists for jobID.
func HasLiveTranscript(jobID string) bool {
	liveTranscripts.Lock()
	defer liveTranscripts.Unlock()
	_, ok := liveTranscripts.byJob[jobID]
	return ok
}

// LiveTranscriptSince returns a copy of the events of jobID from index from
// on. ok is false when no transcript exists for jobID (dropped or never
// started).
func LiveTranscriptSince(jobID string, from int) (events []LiveEvent, ok bool) {
	liveTranscripts.Lock()
	lt, ok := liveTranscripts.byJob[jobID]
	liveTranscripts.Unlock()
	if !ok {
		return nil, false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	if from < 0 {
		from = 0
	}
	if from >= len(lt.events) {
		return nil, true
	}
	return append([]LiveEvent(nil), lt.events[from:]...), true
}
