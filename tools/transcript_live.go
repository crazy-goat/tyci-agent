package tools

import "sync"

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
// first call. A resumed job gets its earlier transcript back, so the agent
// view keeps the turns before the resume.
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

// RecordLiveEvent appends ev to the transcript of jobID and creates the
// transcript on first use. A subagent records its events through its own
// collector. This entry point lets other packages feed a transcript as well,
// for example the TUI tests of the agent view.
func RecordLiveEvent(jobID string, ev LiveEvent) {
	startLiveTranscript(jobID).add(ev)
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
