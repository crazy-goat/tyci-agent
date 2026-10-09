package bus

import "time"

// The kinds of v0.7.0. Each kind has one payload type, registered below.
const (
	// KindJobStatus is the status of a background job. It is Latest, keyed by job.
	KindJobStatus Kind = "job.status"
	// KindNoticeCompletion tells a parent that a job or a question finished.
	// It also carries the inbox warning.
	KindNoticeCompletion Kind = "notice.completion"
	// KindAskRequest is a question that an agent asks its parent.
	KindAskRequest Kind = "ask.request"
	// KindAgentMessage is a message from one agent to another.
	KindAgentMessage Kind = "agent.message"
	// KindBtwAnswer is the answer of a btw fork.
	KindBtwAnswer Kind = "btw.answer"
	// KindPingMissed tells the parent that an agent has been quiet too long.
	KindPingMissed Kind = "ping.missed"
)

// Completion is the payload of KindNoticeCompletion.
type Completion struct {
	Text  string `json:"text"`
	Agent string `json:"agent,omitempty"`
}

// AskRequest is the payload of KindAskRequest.
type AskRequest struct {
	Agent       string `json:"agent"`
	Question    string `json:"question"`
	QuestionSeq int    `json:"question_seq"`
	Text        string `json:"text"`
}

// AgentMessage is the payload of KindAgentMessage.
type AgentMessage struct {
	Text string `json:"text"`
}

// PingMissed is the payload of KindPingMissed.
type PingMissed struct {
	Agent    string        `json:"agent"`
	QuietFor time.Duration `json:"quiet_for"`
}

// BtwAnswer is the payload of KindBtwAnswer.
type BtwAnswer struct {
	Question string `json:"question"`
	Text     string `json:"text"`
	JobID    string `json:"job_id"`
}

// JobStatus is the payload of KindJobStatus. It is a bus-local copy of the
// fields that the TUI jobs panel reads, so that bus does not import jobs.
type JobStatus struct {
	ID       string    `json:"id"`
	ParentID string    `json:"parent_id"`
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	Progress string    `json:"progress"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended"`
	Question bool      `json:"question"`
}

func init() {
	Register[JobStatus](KindJobStatus, Latest, jobStatusKey)
	Register[Completion](KindNoticeCompletion, Durable, nil)
	Register[AskRequest](KindAskRequest, Durable, nil)
	Register[AgentMessage](KindAgentMessage, Durable, nil)
	Register[BtwAnswer](KindBtwAnswer, Durable, nil)
	Register[PingMissed](KindPingMissed, Durable, nil)
}

// jobStatusKey coalesces job.status messages per job.
func jobStatusKey(j JobStatus) string { return "job:" + j.ID }
