package flow

import (
	"context"
	"time"
)

// Workflow is a JSON state machine: states and named transitions.
// It never calls a model itself; models run only inside agent states
// through AgentRunner.
type Workflow struct {
	Name     string           `json:"name"`
	Start    string           `json:"start"`
	Defaults Defaults         `json:"defaults"`
	States   map[string]State `json:"states"`
}

// Defaults holds workflow-wide defaults.
type Defaults struct {
	MaxVisits int `json:"max_visits"`
}

// State is one node of the workflow graph.
// Exactly one of Check, Agent, Ask, End must be set. Task names the task
// template for an agent state and does not count as a kind by itself.
type State struct {
	Check      string            `json:"check,omitempty"`
	Agent      string            `json:"agent,omitempty"`
	Task       string            `json:"task,omitempty"`
	Ask        string            `json:"ask,omitempty"`
	End        bool              `json:"end,omitempty"`
	On         map[string]string `json:"on,omitempty"`
	MaxVisits  int               `json:"max_visits,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
}

// Ask is the pause payload stored in RunState when the run waits for a human.
type Ask struct {
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
}

// RunState is the persisted run state (state.json, version 1).
// Reason is an addition to SDR 5.7: why the run failed.
type RunState struct {
	Version   int            `json:"version"` // 1
	Run       string         `json:"run"`
	Workflow  string         `json:"workflow"`
	Repo      string         `json:"repo"`
	Issue     int            `json:"issue"`
	Branch    string         `json:"branch"`
	Worktree  string         `json:"worktree"`
	Status    string         `json:"status"` // running|paused|done|failed
	Reason    string         `json:"reason,omitempty"`
	Current   string         `json:"current"`
	Ask       *Ask           `json:"ask,omitempty"`
	PR        int            `json:"pr,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Visits    map[string]int `json:"visits"`
	History   []Step         `json:"history"`
}

// Step is one executed transition.
type Step struct {
	Seq        int       `json:"seq"`
	State      string    `json:"state"`
	Kind       string    `json:"kind"` // check|agent|ask
	Key        string    `json:"key"`
	To         string    `json:"to"`
	StartedAt  time.Time `json:"started_at"`
	EndedAt    time.Time `json:"ended_at"`
	Exit       *int      `json:"exit,omitempty"`
	Role       string    `json:"role,omitempty"`
	Session    string    `json:"session,omitempty"`
	StderrTail string    `json:"stderr_tail,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
}

// RunContext is the template fields of SDR 5.4 passed to agent states.
// DefaultBranch and RunDir are empty until later issues wire them;
// the runner fills only what it currently knows.
type RunContext struct {
	Repo          string
	Branch        string
	DefaultBranch string
	Worktree      string
	RunDir        string
	Reason        string
	StateName     string
	Issue         int
	PR            int
	Visit         int
}

// CheckResult is the outcome of one check-script execution.
type CheckResult struct {
	Exit       *int
	StderrTail string
	Stdout     string
}

// CheckRunner executes a check state and resolves its transition key.
type CheckRunner interface {
	Run(ctx context.Context, s State, env []string, dir string) (key string, res CheckResult, err error)
}

// AgentRunner executes an agent state through the subagent code.
type AgentRunner interface {
	Run(ctx context.Context, role, task string, rc RunContext) (key, sessionID string, err error)
}

// StateSaver persists the run state after every transition.
// Store is the file implementation.
type StateSaver interface {
	Save(*RunState) error
}

// Runner walks a Workflow. It never calls a model itself.
type Runner struct {
	WF     *Workflow
	Checks CheckRunner
	Agents AgentRunner
	Store  StateSaver
	RunDir string // run directory: TYCI_RUN_DIR and RunContext.RunDir
	Notify func(string)
	OnSkip func(*RunState)
}
