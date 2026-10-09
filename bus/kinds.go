package bus

// The kinds of v0.7.0. Each kind has one payload type, registered below.
const (
	// KindNoticeCompletion tells a parent that a job or a question finished.
	// It also carries the inbox warning.
	KindNoticeCompletion Kind = "notice.completion"
	// KindAskRequest is a question that an agent asks its parent.
	KindAskRequest Kind = "ask.request"
	// KindAgentMessage is a message from one agent to another.
	KindAgentMessage Kind = "agent.message"
	// KindBtwAnswer is the answer of a btw fork.
	KindBtwAnswer Kind = "btw.answer"
)

// Completion is the payload of KindNoticeCompletion.
type Completion struct {
	Text  string `json:"text"`
	Agent string `json:"agent,omitempty"`
	// Quiet notices wait for the next drain and do not wake an idle chat.
	Quiet bool `json:"quiet,omitempty"`
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

// BtwAnswer is the payload of KindBtwAnswer.
type BtwAnswer struct {
	Question string `json:"question"`
	Text     string `json:"text"`
	JobID    string `json:"job_id"`
}

func init() {
	Register[Completion](KindNoticeCompletion, Durable, nil)
	Register[AskRequest](KindAskRequest, Durable, nil)
	Register[AgentMessage](KindAgentMessage, Durable, nil)
	Register[BtwAnswer](KindBtwAnswer, Durable, nil)
}
