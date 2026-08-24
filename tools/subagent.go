package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/internal/agentdefs"
	"github.com/decodo/tyci/internal/worktree"
	"github.com/decodo/tyci/stream"
)

// Subagents do not get an implicit wall-clock or iteration backstop. Their
// contexts remain cancelable through the jobs registry (kill_job), and a
// blocking foreground call may still hand work to the background after
// SubagentBackgroundAfterSec.
//
// These constants remain as deprecated compatibility names for callers that
// used them when constructing schemas or wait durations. They are not used to
// cancel subagent execution.
const (
	SubagentTimeoutSec    = 1800
	SubagentMinTimeoutSec = 120
	SubagentMaxTimeoutSec = SubagentTimeoutSec
)

// SubagentBackgroundAfterSec is how long a blocking subagent call waits before
// handing its children to the background — the same handoff the bash tool does
// at 30s.
//
// 60s rather than 30 because a child has a model round trip and a few tool
// calls to make before it could possibly be finished, so a shorter window
// would background nearly every call and cost the parent a notice it did not
// need. What the handoff buys is the thing the parent cannot buy for itself:
// the turn ends, so the person at the keyboard gets their prompt back and can
// carry on talking instead of watching a spinner.
//
// A var, not a const, so tests can shrink it instead of waiting out a real
// 60s timer to exercise the timer.C exit of runWithHandoff's select.
var SubagentBackgroundAfterSec = 60 * time.Second

// ErrSubagentTruncated and ErrSubagentTimedOut remain compatibility sentinels
// for custom runners and previously persisted results. The built-in subagent
// runner no longer creates either condition: children stop only when their
// context is cancelled or when they finish normally.
var ErrSubagentTruncated = errors.New("subagent hit its max-iterations cap")
var ErrSubagentTimedOut = errors.New("subagent exceeded its wall-clock time limit")

// ErrSubagentStoppedByUser is the kill-switch counterpart to the two
// sentinels above: returned (wrapped via fmt.Errorf %w) by a SubAgentRunner
// when the child was stopped through jobs.Registry.Cancel — today only
// kill_job reaches it. Treated like them downstream (partial success with
// Truncated=true, carrying whatever text the child produced first, plus a
// resumable entry in main.go) because from the caller's side "cut off by
// time" and "stopped by user" need the same response: read what's there,
// and resume() if more is needed. Sentinel lives in tools/ so the layering
// stays: tools has no upward dependency on agent or jobs.
var ErrSubagentStoppedByUser = errors.New("subagent was stopped by user (kill_job)")

type SubagentTool struct {
	Runner SubAgentRunner
}

func (t *SubagentTool) Name() string { return "subagent" }

// JobHandle is the minimal handle Start returns — just enough for runAsync
// to report the assigned id back to the model.
type JobHandle interface{ ID() string }

// JobStarter is the async-spawn counterpart to tools.JobWaiter (see
// wait.go): a local, minimal contract so this package never imports "jobs"
// (same import-cycle rationale as JobWaiter's doc comment). main() supplies
// an adapter over the app's shared jobs.Registry via SetJobStarter.
type JobStarter interface {
	// kind and parentID are threaded straight onto the created jobs.Job (see
	// jobs.Kind / jobs.Job.ParentID) — this package cannot set them itself
	// since it never imports "jobs" (same layering rule as the rest of this
	// file). kind is one of the JobKind* string constants below; parentID is
	// normally read from ctx.Value(JobIDCtxKey{}) at the call site, i.e. "the
	// job this call is running inside, if any".
	Start(ctx context.Context, description, kind, parentID string, fn func(ctx context.Context, jobID string) (result string, truncated bool, err error)) JobHandle
}

// JobKindSubagent and JobKindBash mirror jobs.KindSubagent/jobs.KindBash as
// plain strings, so this package can pass a kind through JobStarter.Start
// without importing "jobs". jobStarterAdapter (main.go) converts them back
// to jobs.Kind on the other side.
const (
	JobKindSubagent = "subagent"
	JobKindBash     = "bash"
	JobKindCron     = "cron"
)

// JobIDCtxKey is the context key under which an async job's own ID is
// stashed (see runAsync) so tools invoked from inside that job — "ask_parent"
// and "report_progress" — can find out "which job am I running inside"
// without this package importing "jobs" (same layering rule as
// JobWaiter/JobStarter above). /btw side-conversations set the same key (see
// btw.go's startBtw) so ask_parent/report_progress work there too, for free.
type JobIDCtxKey struct{}

// TodoAgentCtxKey is the context key under which a subagent's own todo-list
// identity is stashed (see runSingleTask below), mirroring JobIDCtxKey
// above: it lets the "todo" tool (tools/todo.go) find out "which agent's
// list am I reading/writing" without keying anything off the *TodoTool
// receiver, which is a single stateless value shared by the whole registry
// (tools/tool.go). Every subagent call — sync or async, top-level or
// nested — gets a freshly minted id here, so a child's todos never land in
// its parent's list, its parent's never land in the child's, and sibling
// children never collide ids with each other.
type TodoAgentCtxKey struct{}

// todoAgentIDCounter backs nextTodoAgentID, mirroring jobs.Registry's own
// timestamp+counter id scheme (see nextID in jobs/registry.go).
var todoAgentIDCounter uint64

// nextTodoAgentID returns a fresh, process-unique id for one subagent's
// todo list. Unique within a single process is all that's required —
// nothing here needs to survive a restart or be comparable across runs.
func nextTodoAgentID() string {
	n := atomic.AddUint64(&todoAgentIDCounter, 1)
	return fmt.Sprintf("subagent-todo-%d-%d", time.Now().UnixNano(), n)
}

// streamStopCtxKey carries the flag that tells a child's streaming collector
// to stop forwarding output to the parent's tool block.
//
// It is set when a blocking call hands its children to the background: the
// parent's tool call has returned by then and the TUI has closed that block,
// so forwarding into it would paint over finished output — the same hazard the
// bash tool solves with its own handed flag. It travels through the context
// because the collector is built three call levels down.
type streamStopCtxKey struct{}

// jobStarter is nil until SetJobStarter is called; runAsync fails loudly
// (not silently blocking or panicking) until then.
var jobStarter JobStarter

// SetJobStarter wires the "subagent" tool's async=true spawn path to a
// JobStarter. Called once from main() with an adapter over the app's shared
// jobs.Registry — the same registry /btw side-conversations and the "wait"
// tool's job_id polling (SetJobWaiter) run on, so a job started here is
// pollable from anywhere.
func SetJobStarter(s JobStarter) { jobStarter = s }

// subagentTask represents a single task for a subagent.
type subagentTask struct {
	Task          string `json:"task"`
	Agent         string `json:"agent,omitempty"`
	Model         string `json:"model,omitempty"`
	MaxIterations *int   `json:"max_iterations,omitempty"`
	// Timeout is accepted for compatibility with older callers, but subagent
	// execution no longer applies a wall-clock limit from this field.
	Timeout *int `json:"timeout,omitempty"`
	// Async, when true, makes the tool register a background job and return
	// its id immediately instead of blocking until the task finishes. See
	// SubagentTool.Run and runAsync.
	Async bool `json:"async,omitempty"`
	// Isolation, when "worktree", gives the child its own checkout of the
	// repository on its own branch instead of sharing the parent's working
	// directory. See runSingleTask's isolate helper.
	Isolation string `json:"isolation,omitempty"`
	// InheritHistory, when true, seeds the child with the parent
	// conversation's history up to this call (see
	// connector.ConversationFromContext) instead of starting it from just
	// Task — a model-initiated version of session forking (see
	// session.ForkAtIndex/ForkAtEventID for the human-triggered paths).
	// Ignored (no history to inherit) when the context carries none, e.g. a
	// call made outside a running agent.Run round.
	InheritHistory bool `json:"inherit_history,omitempty"`
}

// subagentResult holds the outcome of one subagent execution.
type subagentResult struct {
	Task      string       `json:"task"`
	Success   bool         `json:"success"`
	Content   string       `json:"content,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Error     string       `json:"error,omitempty"`
	ToolCalls int          `json:"tool_calls"`
	Usage     stream.Usage `json:"usage"`
	Model     string       `json:"model,omitempty"`
	// Truncated is true when the child ran to its MaxIterations cap and
	// self-stopped with a partial answer (still a "success" but flagged so
	// the parent distinguishes from a clean completion).
	Truncated bool `json:"truncated,omitempty"`
}

// collector implements display.Display and captures all output (thread-safe).
type collector struct {
	mu        sync.Mutex
	thinking  strings.Builder
	text      strings.Builder
	toolCalls int
	usage     stream.Usage
	err       error
}

func newCollector() *collector { return &collector{} }

func (c *collector) Request(content string) {}
func (c *collector) Thinking(text string) {
	c.mu.Lock()
	c.thinking.WriteString(text)
	c.mu.Unlock()
}
func (c *collector) Text(text string) {
	c.mu.Lock()
	c.text.WriteString(text)
	c.mu.Unlock()
}
func (c *collector) ToolCallStart(name string) {
	c.mu.Lock()
	c.toolCalls++
	c.mu.Unlock()
}
func (c *collector) ToolCallDelta(delta string)      {}
func (c *collector) ToolCallEnd(name, result string) {}
func (c *collector) ToolFinish()                     {}
func (c *collector) ToolBlock(msg string)            {}
func (c *collector) Summary(usage stream.Usage, stats stream.Stats) {
	c.mu.Lock()
	c.usage = usage
	c.mu.Unlock()
}
func (c *collector) Total(usage stream.Usage) {}
func (c *collector) Error(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
}
func (c *collector) End() {}

func (c *collector) Result() subagentResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return subagentResult{
		Content:   c.text.String(),
		Thinking:  c.thinking.String(),
		ToolCalls: c.toolCalls,
		Usage:     c.usage,
	}
}

// SubagentSink is the Sink-shaped callback set the runner (main.go's
// agentRunner) must drive when executing a child agent, so the child's
// Text/Thinking calls actually reach *streamingCollector's forwarding logic
// and stream live to the parent TUI's subagent modal instead of only
// appearing once the whole task finishes. It is passed to the runner via
// context (SubagentSinkCtxKey), not a RunTask parameter, and the runner
// asserts it to agent.Sink itself — defined structurally here (matching
// agent.Sink's method set) so this package never has to import agent (see
// ErrSubagentTruncated's comment on layering).
type SubagentSink interface {
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
	// CollectedText returns the text accumulated so far via Text calls, so
	// the runner can read back the final answer after agent.Run completes.
	CollectedText() string
}

// SubagentSinkCtxKey is the context key under which runSingleTask stores the
// SubagentSink for the runner to pick up and drive directly, instead of
// building its own non-forwarding Sink.
type SubagentSinkCtxKey struct{}

// streamingCollector wraps collector and pushes output to stream.OnOutput
// for live TUI display in the subagent modal.
type streamingCollector struct {
	*collector
	toolIdx int
	mu      sync.Mutex
	lineBuf strings.Builder // buffer for partial lines

	// parentOutput saves the stream.OnOutput callback from the parent's
	// runOnce so we can forward output to the TUI even after the subagent's
	// own runOnce overwrites the global stream.OnOutput.
	parentOutput func(toolIdx int, line string)

	// stop, once set, ends the forwarding: the parent's tool call has returned
	// and its block is closed, so anything sent now would paint over finished
	// output. See streamStopCtxKey.
	stop *atomic.Bool

	// jobID, when non-empty, is this job's own id (from JobIDCtxKey) —
	// captured at creation so Text/Thinking/ToolCallStart/ToolCallDelta/
	// ToolCallEnd can touch the job's "last activity" timestamp via
	// touchJobActivity without a context lookup on every call. Empty for a
	// subagent call that was never handed a job id (e.g. a blocking call
	// under a mode with backgrounding disabled).
	jobID string
}

func newStreamingCollector(ctx context.Context, toolIdx int) *streamingCollector {
	// Capture the parent's streaming callback from context before the
	// subagent's inner runOnce installs its own.
	parentOutput := stream.Output(ctx)
	stop, _ := ctx.Value(streamStopCtxKey{}).(*atomic.Bool)
	jobID, _ := ctx.Value(JobIDCtxKey{}).(string)
	return &streamingCollector{
		collector:    newCollector(),
		toolIdx:      toolIdx,
		parentOutput: parentOutput,
		stop:         stop,
		jobID:        jobID,
	}
}

// touchActivity is the single point where this collector reports "I'm still
// alive" for its own job — see touchJobActivity's doc comment for why this
// is a separate, cheaper path than report_progress.
func (s *streamingCollector) touchActivity() {
	touchJobActivity(s.jobID)
}

func (s *streamingCollector) Text(text string) {
	s.collector.Text(text)
	s.pushText(text)
	s.touchActivity()
}

func (s *streamingCollector) Thinking(text string) {
	s.collector.Thinking(text)
	s.pushText(text)
	s.touchActivity()
}

func (s *streamingCollector) ToolCallStart(name string) {
	s.collector.ToolCallStart(name)
	s.touchActivity()
}

func (s *streamingCollector) ToolCallDelta(delta string) {
	s.collector.ToolCallDelta(delta)
	s.touchActivity()
}

func (s *streamingCollector) ToolCallEnd(name, result string) {
	s.collector.ToolCallEnd(name, result)
	s.touchActivity()
}

// pushText buffers text and pushes complete lines to the parent's streaming
// callback (saved at creation time). We use s.parentOutput instead of the
// global stream.OnOutput because subagent's own runOnce overwrites the global.
func (s *streamingCollector) pushText(text string) {
	if s.parentOutput == nil {
		return
	}
	if s.stop != nil && s.stop.Load() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lineBuf.WriteString(text)
	content := s.lineBuf.String()

	for {
		idx := strings.IndexByte(content, '\n')
		if idx < 0 {
			break
		}
		line := content[:idx]
		s.parentOutput(s.toolIdx, line)
		content = content[idx+1:]
	}
	s.lineBuf.Reset()
	s.lineBuf.WriteString(content)
}

// flushPartial flushes any remaining partial line when subagent finishes.
func (s *streamingCollector) flushPartial() {
	if s.parentOutput == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lineBuf.Len() > 0 {
		s.parentOutput(s.toolIdx, s.lineBuf.String())
		s.lineBuf.Reset()
	}
}

// CollectedText returns the text accumulated so far via Text calls,
// satisfying SubagentSink so the runner can read back the final answer once
// this collector — rather than a separate one — is the actual Sink that
// drove agent.Run.
func (s *streamingCollector) CollectedText() string {
	s.collector.mu.Lock()
	defer s.collector.mu.Unlock()
	return s.collector.text.String()
}

// StreamProgress implements the streamer interface so that the subagent's
// inner runOnce can forward tool output (e.g. bash lines) to the parent TUI.
// toolIdx here is the index within the subagent's own tool batch; we ignore it
// and always forward using the subagent's own toolIdx (the parent's perspective).
func (s *streamingCollector) StreamProgress(_ int, line string) {
	if s.parentOutput == nil {
		return
	}
	s.parentOutput(s.toolIdx, line)
}

func (t *SubagentTool) Run(ctx context.Context, input map[string]any) ToolResult {
	if t.Runner == nil {
		return ToolResult{Type: "result", Success: false, Error: "subagent runner not configured"}
	}

	var defaultModel string
	if mc := connector.ModelClientFromContext(ctx); mc != nil {
		defaultModel = mc.Model()
	}
	if defaultModel == "" {
		return ToolResult{Type: "result", Success: false, Error: "no model specified and no default model set"}
	}

	// Parse tasks
	tasks, err := parseTasks(input, defaultModel)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	if len(tasks) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "no tasks provided"}
	}

	if async, mixed := asyncTaskMode(tasks); mixed {
		return ToolResult{Type: "result", Success: false, Error: "cannot mix async and non-async tasks in the same call; issue separate subagent calls"}
	} else if async {
		return t.runAsync(ctx, tasks)
	}

	// A blocking call still blocks — but not forever. After
	// SubagentBackgroundAfterSec the children carry on in the background and
	// the turn ends, which is the only way the person at the keyboard gets
	// their prompt back. Requires a job registry to hand them to; without one
	// (a one-shot `tyci run`, where there is no next turn to deliver a notice
	// into) the old blocking behaviour is correct and is what happens.
	//
	// runWithHandoff always returns a fully-formed result: either every task
	// finished inside the window and it is the same result runTasks below
	// would have produced, or at least one is still going and it is the
	// handoff message. Either way it is returned directly — the second
	// (bool) value only tells tests and callers which of those happened, it
	// does not gate whether the result is usable. Falling through to runTasks
	// here used to re-run every task from scratch on the common "finished in
	// time" path, paying the model and side effects twice.
	if jobStarter != nil && backgroundAllowed(ctx) {
		res, _ := t.runWithHandoff(ctx, tasks, true)
		return res
	}

	// Reached when backgrounding is disabled for this mode — in practice
	// `tyci run` / `--print` (main.go never calls SetBackgroundBashEnabled
	// there; see commands.go). backgroundAllowed also returns false when ctx
	// carries SubagentSinkCtxKey, but that combination can never actually
	// reach here: subagentDeniedTools (toolgate.go) removes the "subagent"
	// tool itself from a child's schema and its runtime gate, so a child
	// can never make this call in the first place.
	//
	// A job registry is still available here in every real invocation
	// (jobStarter is wired unconditionally in main.go). So the children
	// still get a job id — via the same runWithHandoff machinery, with
	// handoff=false — which is what makes report_progress work on them
	// instead of failing with "no job id". wait(job_id=...) and resume do
	// NOT start working here too: this call blocks until every child is
	// terminal and resultsToToolResult never surfaces the ids, so the model
	// is never told one to begin with.
	//
	// (ask is different again: giving these a job id must NOT make ask
	// block for its full timeout with no way to ever receive an answer —
	// see AskUnroutableCtxKey, which ask consults separately from "do I have
	// a job id".)
	if jobStarter != nil {
		res, _ := t.runWithHandoff(ctx, tasks, false)
		return res
	}

	// No job registry at all — only reachable in tests; the real binary
	// always wires one (main.go). No job ids are possible here, so this is
	// the one remaining plain, unregistered path.
	results := runTasks(ctx, t.Runner, tasks)
	return resultsToToolResult(results)
}

// resultsToToolResult converts finished subagent results into the ToolResult
// shape the model sees: a single task collapses to plain text with its
// Truncated flag propagated (unchanged from before batching existed), a
// batch becomes a JSON array. Shared by the plain runTasks path and by
// runWithHandoff's all-finished-inline case, so the two cannot drift apart.
func resultsToToolResult(results []subagentResult) ToolResult {
	if len(results) == 1 {
		r := results[0]
		if !r.Success {
			return ToolResult{Type: "result", Success: false, Error: r.Error}
		}
		return ToolResult{Type: "result", Success: true, Content: r.Content, Truncated: r.Truncated}
	}

	data, err := json.Marshal(results)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("marshal results: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: string(data)}
}

// parseTasks extracts tasks from input.
// Accepts:
//
//	{ "task": "..." }                                    — single task, current model
//	{ "task": "...", "model": "..." }                     — single task with model override
//	{ "tasks": [{ "task": "...", "model": "..." }, ...] } — parallel tasks
func parseTasks(input map[string]any, defaultModel string) ([]subagentTask, error) {
	// Try "tasks" array first
	if tasksRaw, ok := input["tasks"]; ok {
		arr, ok := tasksRaw.([]any)
		if !ok {
			return nil, fmt.Errorf("tasks must be an array")
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("tasks array is empty")
		}
		var tasks []subagentTask
		for i, item := range arr {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("tasks[%d] must be an object", i)
			}
			t, err := taskFromMap(m)
			if err != nil {
				return nil, fmt.Errorf("tasks[%d]: %w", i, err)
			}
			tasks = append(tasks, t)
		}
		return tasks, nil
	}

	// Single task from "task" field
	taskStr, ok := input["task"].(string)
	if !ok || taskStr == "" {
		return nil, fmt.Errorf("task or tasks required")
	}

	t := subagentTask{Task: taskStr}
	if agent, ok := input["agent"].(string); ok && agent != "" {
		t.Agent = agent
	}
	if m, ok := input["model"].(string); ok && m != "" {
		t.Model = m
	}
	if mi, ok := input["max_iterations"]; ok {
		v, err := toInt(mi)
		if err != nil {
			return nil, fmt.Errorf("max_iterations: %w", err)
		}
		t.MaxIterations = &v
	}
	if timeout, ok := input["timeout"]; ok {
		v, err := parseSubagentTimeout(timeout)
		if err != nil {
			return nil, fmt.Errorf("timeout: %w", err)
		}
		t.Timeout = &v
	}
	if a, ok := input["async"].(bool); ok {
		t.Async = a
	}
	if ih, ok := input["inherit_history"].(bool); ok {
		t.InheritHistory = ih
	}
	return []subagentTask{t}, nil
}

// asyncTaskMode reports whether the batch should run async: async is true
// only when every task requested it, mixed is true when some but not all
// did (an ambiguous request the caller must reject rather than guess at).
func asyncTaskMode(tasks []subagentTask) (async bool, mixed bool) {
	n := 0
	for _, t := range tasks {
		if t.Async {
			n++
		}
	}
	if n == 0 {
		return false, false
	}
	if n == len(tasks) {
		return true, false
	}
	return false, true
}

// toInt accepts any JSON number preserved by encoding/json (float64) or a
// plain int (from tests / typed callers). Rejects NaN/Inf and values outside
// the int range so a runaway model value (e.g. max_iterations: 1e20) doesn't
// silently saturate to math.MaxInt64 and let the child run forever.
func toInt(v any) (int, error) {
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, fmt.Errorf("expected integer, got %v", x)
		}
		if x > float64(math.MaxInt) || x < float64(math.MinInt) {
			return 0, fmt.Errorf("value out of int range: %v", x)
		}
		return int(x), nil
	case float32:
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return 0, fmt.Errorf("expected integer, got %v", x)
		}
		f := float64(x)
		if f > float64(math.MaxInt) || f < float64(math.MinInt) {
			return 0, fmt.Errorf("value out of int range: %v", x)
		}
		return int(x), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", v)
	}
}

// parseSubagentTimeout validates the legacy timeout input while keeping it
// accepted by the tool API. The value is intentionally ignored at runtime.
func parseSubagentTimeout(v any) (int, error) {
	return toInt(v)
}

func taskFromMap(m map[string]any) (subagentTask, error) {
	taskStr, _ := m["task"].(string)
	if taskStr == "" {
		return subagentTask{}, fmt.Errorf("task is required")
	}
	t := subagentTask{Task: taskStr}
	if agent, ok := m["agent"].(string); ok && agent != "" {
		t.Agent = agent
	}
	if model, ok := m["model"].(string); ok && model != "" {
		t.Model = model
	}
	if mi, ok := m["max_iterations"]; ok {
		v, err := toInt(mi)
		if err != nil {
			return subagentTask{}, fmt.Errorf("max_iterations: %w", err)
		}
		t.MaxIterations = &v
	}

	if timeout, ok := m["timeout"]; ok {
		v, err := parseSubagentTimeout(timeout)
		if err != nil {
			return subagentTask{}, fmt.Errorf("timeout: %w", err)
		}
		t.Timeout = &v
	}
	if a, ok := m["async"].(bool); ok {
		t.Async = a
	}
	if iso, ok := m["isolation"].(string); ok && iso != "" && iso != "none" {
		if iso != "worktree" {
			return subagentTask{}, fmt.Errorf("isolation: %q is not a mode; use \"worktree\" (own checkout, own branch) or leave it out (share the parent's directory)", iso)
		}
		t.Isolation = iso
	}
	if ih, ok := m["inherit_history"].(bool); ok {
		t.InheritHistory = ih
	}
	return t, nil
}

// runAsync registers each task as a background job via jobStarter and
// returns immediately with the assigned job ids, instead of blocking until
// the tasks finish (runTasks' path). The job's context is detached from ctx
// (context.WithoutCancel) because ctx dies with this tool call's turn, but
// the job must keep running after Run returns. It remains cancelable through
// the jobs registry (kill_job). Poll results via the "wait" tool's job_id mode.
// spawnedTask is one child running as a background job.
//
// The finished/handed pair exists to settle one race with one lock: a blocking
// call's 60s timer can fire at the same moment a child completes, and exactly
// one of the two outcomes must happen — either the result is returned inline
// (the child won) or the parent is notified later (the timer won). Both
// happening means the parent is told twice; neither means the result is lost.
type spawnedTask struct {
	task  subagentTask
	jobID string
	label string
	done  chan struct{}

	// cancel ends this child's own detached context (jobCtx in spawn). The
	// job's closure already defers a call to it on normal completion; a
	// caller that needs to stop the child early (its own parent ctx was
	// cancelled and there is no background to hand it off to) calls it a
	// second time, which is safe — context.CancelFunc is idempotent.
	cancel context.CancelFunc

	mu       sync.Mutex
	finished bool
	handed   bool
	res      subagentResult

	// stopStream is flipped when the task is handed over or cancelled
	// outright, so the child stops painting into a tool block that has
	// closed.
	stopStream *atomic.Bool
}

// finish records the result and reports whether the parent must be notified,
// i.e. whether this task had already been handed to the background.
func (s *spawnedTask) finish(res subagentResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res = res
	s.finished = true
	return s.handed
}

// hand marks the task as backgrounded. It returns false when the task had
// already finished, in which case there is nothing to hand over.
func (s *spawnedTask) hand() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return false
	}
	s.handed = true
	return true
}

// claimForCancel reports whether this task is still worth cancelling, under
// the same lock and with the same "false once finished" shape as hand().
//
// Unlike hand() it records nothing, and that is the whole point: finish()
// (running in the child's own goroutine once the cancelled child actually
// returns) reads handed to decide whether to emit a background-finished /
// FAILED notice, and nothing in cancelRemaining's no-handoff mode ever
// drains one. Setting handed here would report a child as "backgrounded"
// when it was really just killed, and that notice text invites
// wait(job_id=...) in the one mode where no id was ever surfaced to the
// model to call it with. A dedicated "cancelled" field was tried and
// removed: nothing read it, and unread state invites a later reader to
// trust it.
func (s *spawnedTask) claimForCancel() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.finished
}

func (s *spawnedTask) result() subagentResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.res
}

// spawn starts one child as a background job and returns immediately.
//
// handedAtStart is true for an async call, where the parent is told the ids
// and nothing is returned inline; false for a blocking call, which waits and
// only hands over if the child is still going after
// SubagentBackgroundAfterSec.
func (t *SubagentTool) spawn(ctx context.Context, task subagentTask, handedAtStart, streamToParent bool) *spawnedTask {
	st := &spawnedTask{
		task:   task,
		label:  truncateLine(strings.TrimSpace(firstLine(task.Task)), 60),
		done:   make(chan struct{}),
		handed: handedAtStart,
	}

	// Detached from the caller's context: the context of a tool call dies when
	// the call returns, which for a backgrounded child would kill the very work
	// we are keeping alive. The jobs registry supplies cancellation for
	// kill_job; there is deliberately no child-specific deadline here.
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	st.cancel = cancel
	stopStream := &atomic.Bool{}
	st.stopStream = stopStream

	parentID, _ := ctx.Value(JobIDCtxKey{}).(string)
	job := jobStarter.Start(jobCtx, task.Task, JobKindSubagent, parentID, func(runCtx context.Context, jobID string) (string, bool, error) {
		defer cancel()
		defer close(st.done)
		runCtx = context.WithValue(runCtx, JobIDCtxKey{}, jobID)
		runCtx = context.WithValue(runCtx, streamStopCtxKey{}, stopStream)
		res := runSingleTask(runCtx, t.Runner, task, 0, streamToParent)

		// Tell the parent it finished — but only if the parent is no longer
		// waiting for it. A blocking call that got its result inline has
		// already read it, and a notice about it would be noise.
		if st.finish(res) {
			if res.Success {
				notify(fmt.Sprintf("[subagent] %q finished — read it with wait(job_id=%q)", st.label, jobID))
			} else {
				notify(fmt.Sprintf("[subagent] %q FAILED — details via wait(job_id=%q)", st.label, jobID))
			}
		}

		if !res.Success {
			return res.Content, res.Truncated, errors.New(res.Error)
		}
		return res.Content, res.Truncated, nil
	})
	st.jobID = job.ID()
	return st
}

func (t *SubagentTool) runAsync(ctx context.Context, tasks []subagentTask) ToolResult {
	if jobStarter == nil {
		return ToolResult{Type: "result", Success: false, Error: "async subagent spawn unavailable: job registry not configured"}
	}

	spawned := make([]*spawnedTask, 0, len(tasks))
	for _, task := range tasks {
		// streamToParent is false: by the time an async child produces output
		// the parent's tool call has returned and its block is closed. The
		// result lives in the job registry and is read with wait().
		spawned = append(spawned, t.spawn(ctx, task, true, false))
	}
	return ToolResult{Type: "result", Success: true, Content: spawnedJobsMessage(spawned, nil)}
}

// spawnedJobsMessage is what the parent reads after children go to the
// background: the ids as JSON on the first line, then what the notices mean.
//
// inline, when non-empty, is the text for children that finished before the
// handoff — a blocking call can end up with some of each.
//
// The ids alone are not enough. A parent handed a bare list has no way to know
// that a child can block on a question and needs an answer, and a blocked
// child that nobody answers burns its whole wall-clock limit and then discards
// everything it did.
func spawnedJobsMessage(spawned []*spawnedTask, inline []subagentResult) string {
	type entry struct {
		Task  string `json:"task"`
		JobID string `json:"job_id"`
	}
	out := make([]entry, 0, len(spawned))
	for _, st := range spawned {
		out = append(out, entry{Task: st.task.Task, JobID: st.jobID})
	}
	data, err := json.Marshal(out)
	if err != nil {
		data = []byte("[]")
	}

	var b strings.Builder
	b.Write(data)
	if len(inline) > 0 {
		finished, err := json.Marshal(inline)
		if err == nil {
			b.WriteString("\n\nThese finished before the handoff, so their results are here in full:\n")
			b.Write(finished)
		}
	}
	fmt.Fprintf(&b, `

%d task(s) are now running in the background. Get on with work that does not depend on them — and if there is a person waiting, talk to them: the turn is yours again.

You will be told when one finishes, and when one is BLOCKED on a question. Two things are then yours to do:
- A question: relay it — to the user, or genuinely-known info — unless you truly know the answer yourself; never invent one standing in for a human who hasn't replied. Call answer_job(job_id=..., text="..."). It is blocked until you do, and everything it has done is discarded when it times out. This is the only way it can reach you.
- A finish: read the result with wait(job_id=...). Nothing else delivers it.

Do not call wait before you are told: it can only say "still running", which the next notice would have told you for free.`, len(spawned))
	return b.String()
}

// runWithHandoff runs a blocking subagent call as background jobs and waits
// for them — up to SubagentBackgroundAfterSec when handoff is true, or until
// every child finishes (or the parent ctx is cancelled) when it is false.
//
// handoff is true exactly when backgroundAllowed(ctx) held at the call site
// (Run): there is somewhere to hand a still-running child off TO, so a
// person or the model gets a turn back instead of waiting on it. It is false
// for the one other case that still has a job registry to register through
// — in practice `tyci run` / `--print`, which never drains a background
// notice — so there is no handoff to offer: the timer and the
// typing-interrupts-the-wait poll both stay disabled (their channels are left
// nil, which never fires in a select), and a child still running when ctx is
// cancelled is stopped outright via cancelRemaining rather than left to run
// unsupervised in the background.
//
// When handoff is false, AskUnroutableCtxKey is stamped on every spawned
// child's context: this call cannot return to its own caller until every
// child is terminal, so a child blocked in "ask_parent" here could never have its
// question answered no matter how long it waited. See that key's doc
// comment in ask.go.
//
// The returned ToolResult is always ready to hand straight back to the
// model — the caller does not need to fall back to re-running anything. The
// bool distinguishes which of the outcomes produced it: false means every
// child finished inside the window, and the result is built from their
// collected results via resultsToToolResult — the same shape a plain,
// unhanded-off run would have produced, computed once rather than by running
// the tasks again. true means the loop ended some other way — handed to the
// background (handoff=true) or cancelled outright (handoff=false).
func (t *SubagentTool) runWithHandoff(ctx context.Context, tasks []subagentTask, handoff bool) (ToolResult, bool) {
	if !handoff {
		ctx = context.WithValue(ctx, AskUnroutableCtxKey{}, true)
	}

	spawned := make([]*spawnedTask, 0, len(tasks))
	for _, task := range tasks {
		// streamToParent stays true while the parent is still waiting: its
		// tool block is open and the live output is the only sign of progress.
		// spawn's stopStream flag closes that tap at the moment of handoff.
		spawned = append(spawned, t.spawn(ctx, task, false, true))
	}

	waiting := make(map[*spawnedTask]struct{}, len(spawned))
	for _, st := range spawned {
		waiting[st] = struct{}{}
	}

	// timerC and pollC are left nil (never fire in a select) when handoff is
	// false: there is nothing to hand still-running children off to, so
	// neither the "background them after a while" timer nor the
	// "typing ends the wait early" poll applies. ctx.Done() is the only exit
	// this mode has besides every child finishing.
	var timerC <-chan time.Time
	var pollC <-chan time.Time
	if handoff {
		timer := time.NewTimer(SubagentBackgroundAfterSec)
		defer timer.Stop()
		timerC = timer.C

		// A person typing ends the wait early. The children are not touched —
		// they carry on in the background exactly as they would at the 60s
		// mark — but the turn ends, so the question gets an answer instead of
		// sitting behind work the person did not ask to wait for.
		poll := time.NewTicker(userPendingPoll)
		defer poll.Stop()
		pollC = poll.C
	}

	for len(waiting) > 0 {
		// One select per remaining task rather than a WaitGroup: the timer has
		// to be able to interrupt the wait, and a WaitGroup cannot be
		// abandoned half way.
		var next *spawnedTask
		for st := range waiting {
			next = st
			break
		}
		select {
		case <-next.done:
			delete(waiting, next)
		case <-pollC:
			if UserPending() {
				return t.handOff(spawned), true
			}
		case <-timerC:
			return t.handOff(spawned), true
		case <-ctx.Done():
			if handoff {
				// The parent turn was cancelled (Esc). The children are
				// detached and keep going; say so rather than pretending
				// they stopped.
				return t.handOff(spawned), true
			}
			// No handoff is available in this mode, so there is nobody to
			// leave these running for — stop them outright instead of
			// letting them run unsupervised to their SubagentTimeoutSec
			// backstop after the caller has already stopped listening.
			return t.cancelRemaining(spawned), true
		}
	}

	// Every child finished inside the window: collect what they produced
	// instead of discarding it and letting the caller run them all again.
	// st.result() is safe to read here without the lock racing finish() —
	// <-next.done only fired above after finish() had already recorded the
	// result (spawn's closure calls finish before its deferred close(done)).
	results := make([]subagentResult, len(spawned))
	for i, st := range spawned {
		results[i] = st.result()
	}
	return resultsToToolResult(results), false
}

// handOff moves whatever is still running to the background and builds the
// message: ids for those, full results for those that already finished.
func (t *SubagentTool) handOff(spawned []*spawnedTask) ToolResult {
	var stillRunning []*spawnedTask
	var finished []subagentResult
	for _, st := range spawned {
		if st.hand() {
			if st.stopStream != nil {
				st.stopStream.Store(true)
			}
			stillRunning = append(stillRunning, st)
			continue
		}
		finished = append(finished, st.result())
	}
	if len(stillRunning) == 0 {
		// Everything finished between the timer firing and this loop. Rare,
		// and there is nothing to hand over — but the caller has already
		// committed to the handoff path, so report the results here. Same
		// shaping as every other all-finished case (resultsToToolResult), so
		// a single task collapses to plain text instead of a one-element
		// JSON array just because it happened to finish on this side of the
		// handoff decision.
		return resultsToToolResult(finished)
	}
	return ToolResult{Type: "result", Success: true, Content: spawnedJobsMessage(stillRunning, finished)}
}

// cancelRemaining is runWithHandoff's handoff=false counterpart to handOff:
// there is no background to move a still-running child to, so instead every
// child not yet finished is cancelled outright via its own spawn-time
// cancel — the same signal spawn's own deferred cancel() sends on normal
// completion, calling it early here is safe since context.CancelFunc is
// idempotent — and the call returns immediately with whatever finished
// results already exist plus a clear error about the rest, rather than
// silently discarding them or leaving them to run unsupervised.
//
// claimForCancel (not hand()) gives the same exactly-once bookkeeping
// against a concurrent finish() without flipping handed — flipping handed
// is what makes finish() emit a background-finished/FAILED notice, and
// nothing in this no-handoff mode drains one; see claimForCancel's doc
// comment. stopStream is flipped here too, same as handOff, so a cancelled
// child stops painting into a tool block this call is about to return from.
func (t *SubagentTool) cancelRemaining(spawned []*spawnedTask) ToolResult {
	var finished []subagentResult
	var cancelled int
	for _, st := range spawned {
		if st.claimForCancel() {
			cancelled++
			if st.stopStream != nil {
				st.stopStream.Store(true)
			}
			if st.cancel != nil {
				st.cancel()
			}
			continue
		}
		finished = append(finished, st.result())
	}
	if cancelled == 0 {
		// Every child finished between ctx firing and this loop running.
		return resultsToToolResult(finished)
	}
	if len(finished) == 0 {
		return ToolResult{
			Type:    "result",
			Success: false,
			Error:   "the call was cancelled before any child finished; the still-running children were cancelled too",
		}
	}
	data, err := json.Marshal(finished)
	if err != nil {
		data = []byte("[]")
	}
	return ToolResult{
		Type:    "result",
		Success: false,
		Error:   fmt.Sprintf("the call was cancelled with %d child(ren) still running (now cancelled); %d already finished: %s", cancelled, len(finished), data),
	}
}

func runTasks(ctx context.Context, runner SubAgentRunner, tasks []subagentTask) []subagentResult {
	results := make([]subagentResult, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t subagentTask) {
			defer wg.Done()
			results[idx] = runSingleTask(ctx, runner, t, 0, true)
		}(i, task)
	}

	wg.Wait()
	return results
}

// streamToParent controls whether the child's output is forwarded live to
// the parent TUI's tool block (via stream.ToolIdxCtxKey / stream.Output).
// It must be false for a job spawned by runAsync: by the time the job
// finishes, the parent's "subagent" tool call has already returned and the
// TUI has closed that tool block, so toolIdx no longer refers to anything
// live — forwarding into it would write to a stale or reused block instead
// of just doing nothing. Job.Result (collected by the plain, non-streaming
// collector below) remains the source of truth for async output, retrieved
// later via the "wait" tool.
func runSingleTask(ctx context.Context, runner SubAgentRunner, task subagentTask, timeoutSec int, streamToParent bool) subagentResult {
	// Resolve the named agent definition, if any, before anything else. An
	// unknown agent name used to silently degrade to a plain subagent with
	// the parent's defaults — indistinguishable from success and a trap for
	// a typo'd name. Fail loudly instead.
	var def agentdefs.Def
	if task.Agent != "" {
		var ok bool
		def, ok = agentdefs.Get("", task.Agent)
		if !ok {
			return subagentResult{
				Task:    task.Task,
				Success: false,
				Error:   fmt.Sprintf("agent %q not found (looked in ~/.tyci/agents and ./.tyci/agents)", task.Agent),
			}
		}
	}

	// Isolation, before the timeout: creating a checkout is a local git
	// operation, but it should not eat into the child's own budget, and a
	// failure here must be reported as a failed task rather than silently
	// letting the child loose in the parent's working tree.
	var wt *worktree.Worktree
	if task.Isolation == "worktree" {
		var err error
		wt, err = worktree.Add(ctx, Workdir(ctx), task.Task)
		if err != nil {
			return subagentResult{
				Task:    task.Task,
				Success: false,
				Error:   fmt.Sprintf("isolation: could not create a worktree, so the task was not started (running it in the shared directory would defeat the point): %v", err),
			}
		}
		defer func() { finishWorktree(ctx, wt) }()
	}

	// timeoutSec is retained in this private signature for source compatibility
	// with existing tests/callers, but subagent runs no longer create a
	// child-specific deadline. Parent cancellation and kill_job remain intact.
	runCtx := ctx

	// The child's isolated HTTP connection pool is created by the runner (see
	// agentRunner.run in main.go), which binds it to the provider it resolves.
	// It used to be stuffed into runCtx here; the transport is no longer
	// something this package has to know about.

	// Model precedence: explicit per-task override wins, then the named
	// agent's frontmatter `model`, then the parent's model inherited via
	// context. Before this, def.Model was read nowhere — a named agent
	// declared as a cheap model still burned the parent's (often expensive)
	// model on every call.
	mName := task.Model
	if mName == "" {
		mName = def.Model
	}
	if mName == "" {
		if mc := connector.ModelClientFromContext(ctx); mc != nil {
			mName = mc.Model()
		}
	}

	// max_iterations remains parsed and carried by subagentTask for API
	// compatibility, but subagent execution is always unlimited. In particular,
	// named-agent frontmatter cannot reintroduce a child iteration cap.

	// Temperature and Fallback are read straight off the named agent's
	// definition — unlike Model/MaxIterations there is no per-task override
	// for either (the subagent tool schema deliberately does not expose
	// them; see tools.go's "subagent" schema comment), so def is the only
	// source. When task.Agent is empty, def is the zero value: Temperature
	// is nil and Fallback is nil, i.e. nothing changes for a plain subagent.
	opts := SubagentOptions{
		// MaxIterations is deliberately not populated: the runner is unlimited.
		Tools:            def.Tools,
		Temperature:      def.Temperature,
		MaxTokens:        def.MaxTokens,
		Fallbacks:        def.Fallback,
		SystemPromptMode: def.SystemPromptMode,
	}
	if task.InheritHistory {
		opts.History = connector.ConversationFromContext(ctx)
	}

	// Get tool index for streaming (passed by agent.executeTools). Only
	// resolved when streamToParent, so an async job never picks up the
	// parent's (soon to be closed) toolIdx or its stream.Output callback —
	// see runSingleTask's doc comment.
	var c *streamingCollector
	if streamToParent {
		toolIdx := 0
		if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
			toolIdx = idx
		}
		c = newStreamingCollector(ctx, toolIdx)
	} else {
		c = &streamingCollector{collector: newCollector()}
	}

	// Hand the runner our forwarding Sink via context (see SubagentSink's
	// doc comment) so it drives agent.Run with this collector directly,
	// instead of building its own non-forwarding one — that was the actual
	// bug behind the subagent modal staying blank while running: c existed
	// but nothing ever called its Text/Thinking.
	runCtx = context.WithValue(runCtx, SubagentSinkCtxKey{}, c)

	// Give this child its own todo-list identity (see TodoAgentCtxKey's doc
	// comment) — a fresh one on every call, so a child never inherits or
	// collides with its parent's or a sibling's plan. This covers both the
	// blocking path (runTasks -> runSingleTask directly) and the async path
	// (runAsync's job body also calls runSingleTask), since both funnel
	// through here.
	//
	// MarkTodoAgentDone is deferred right here, not wrapped around the
	// runner call below: this id is only ever known inside this function,
	// and every return path from here on (including the early ones above
	// this point never reach it — they return before a list could exist)
	// must mark the list terminal so it becomes eligible for eviction once
	// this child is done, without risking a still-running child's list
	// ever being dropped.
	todoAgentID := nextTodoAgentID()
	runCtx = context.WithValue(runCtx, TodoAgentCtxKey{}, todoAgentID)
	defer MarkTodoAgentDone(todoAgentID)

	// Every tool resolves relative paths against Workdir (see workdir.go), so
	// this one line is what makes the child's reads, writes and shell commands
	// land in its own checkout.
	if wt != nil {
		runCtx = WithWorkdir(runCtx, wt.Dir)
	}

	// Run the task via the runner interface. def.SystemPrompt is empty when
	// no agent was named (def is the zero value), so this also covers the
	// plain-subagent case without a separate branch on task.Agent.
	var content string
	var err error
	if def.SystemPrompt != "" {
		content, err = runner.RunTaskWithSystem(runCtx, task.Task, mName, def.SystemPrompt, opts)
	} else {
		content, err = runner.RunTask(runCtx, task.Task, mName, opts)
	}

	// Flush any remaining partial line
	c.flushPartial()

	res := c.Result()
	res.Task = task.Task
	res.Model = mName
	res.Content = content

	if err != nil {
		res.Success = false
		switch {
		case errors.Is(err, ErrSubagentTruncated), errors.Is(err, ErrSubagentTimedOut), errors.Is(err, ErrSubagentStoppedByUser):
			// Hit the iteration cap or the wall-clock deadline but produced
			// text: surface as a partial success, not an error, the same
			// way either way. Stopped-by-user (kill_job) joins them: what
			// the child produced before being stopped is real partial work,
			// and main.go already stashed a resumable entry for it. The
			// content already carries the [note: ...] resume hint appended
			// by the runner (main.go's agentRunner.run).
			res.Success = true
			res.Truncated = true
		case timeoutSec > 0 && errors.Is(err, context.DeadlineExceeded):
			// A deadline hit that the runner did NOT wrap as
			// ErrSubagentTimedOut (e.g. a runner implementation other than
			// main.go's, or a hard failure before any text existed to
			// carry) surfaces as a bare "context deadline exceeded";
			// make it actionable so the parent understands the child was
			// cut off, not that its task was malformed.
			res.Error = fmt.Sprintf("subagent exceeded its %ds time limit and was stopped; narrow the task or split it", timeoutSec)
		default:
			res.Error = err.Error()
		}
	} else {
		res.Success = true
	}

	if wt != nil {
		res.Content += "\n\n" + worktreeNote(ctx, wt)
	}

	return res
}

// worktreeNote tells the parent where the child's work ended up. Without it
// the parent has a plausible answer and no idea that the files it describes
// are on a branch nobody has looked at.
func worktreeNote(ctx context.Context, wt *worktree.Worktree) string {
	changed, err := wt.Changed(ctx)
	switch {
	case err != nil:
		return fmt.Sprintf("[isolation: worked in %s on branch %s; could not tell whether anything changed (%v), so the branch was kept]", wt.Dir, wt.Branch, err)
	case changed:
		return fmt.Sprintf("[isolation: this ran in its own checkout, so nothing above is in your working tree yet. The changes are on branch %s (checkout at %s). Review them with: git diff %s..%s]", wt.Branch, wt.Dir, wt.BaseCommit, wt.Branch)
	default:
		return "[isolation: ran in its own checkout and changed no files, so the checkout and its branch were removed]"
	}
}

// finishWorktree keeps a checkout that holds work and removes one that does
// not. Detached from the caller's context on purpose: by the time this runs
// the child may have been cancelled or timed out, and cleaning up with a dead
// context would leave a directory and a branch behind for every such run.
func finishWorktree(ctx context.Context, wt *worktree.Worktree) {
	cleanupCtx := context.WithoutCancel(ctx)
	changed, err := wt.Changed(cleanupCtx)
	if err == nil && !changed {
		_ = wt.Remove(cleanupCtx)
	}
}
