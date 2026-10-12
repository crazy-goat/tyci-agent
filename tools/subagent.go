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

	"github.com/crazy-goat/tyci-agent/connector"
	"github.com/crazy-goat/tyci-agent/internal/agentdefs"
	"github.com/crazy-goat/tyci-agent/internal/worktree"
	"github.com/crazy-goat/tyci-agent/session"
	"github.com/crazy-goat/tyci-agent/stream"
)

// Subagents do not get an implicit wall-clock or iteration backstop. Their
// contexts remain cancelable through the jobs registry (kill_job). A blocking
// call waits for its children; it never hands them to the background.
//
// These constants remain as deprecated compatibility names for callers that
// used them when constructing schemas or wait durations. They are not used to
// cancel subagent execution.
const (
	SubagentTimeoutSec    = 1800
	SubagentMinTimeoutSec = 120
	SubagentMaxTimeoutSec = SubagentTimeoutSec
)

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

// ErrCancelledByParent is the cancel cause of a child that cancelRemaining
// stops because its blocking call ended. It is not a kill_job stop, so
// isStoppedByUser (main.go) reads it through context.Cause.
var ErrCancelledByParent = errors.New("subagent call ended; the child was cancelled by its parent")

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
// It is set when a cancelled child must stop: the parent's tool call returns
// at once then, and the TUI closes that block, so forwarding into it would
// paint over finished output. The async path sets it too, once its tool call
// has returned. It travels through the context because the collector is built
// three call levels down.
type streamStopCtxKey struct{}

// jobStarter is nil until SetJobStarter is called; runAsync fails loudly
// (not silently blocking or panicking) until then. Guarded by jobStarterMu
// for the same reason jobNotifier is (see bgbash.go's jobNotifierMu doc
// comment): it is read from job goroutines that outlive the tool call that
// started them, while SetJobStarter is called from the setup path.
var (
	jobStarterMu sync.RWMutex
	jobStarter   JobStarter
)

// SetJobStarter wires the "subagent" tool's async=true spawn path to a
// JobStarter. Called once from main() with an adapter over the app's shared
// jobs.Registry — the same registry /btw side-conversations and the "wait"
// tool's job_id polling (SetJobWaiter) run on, so a job started here is
// pollable from anywhere.
func SetJobStarter(s JobStarter) {
	jobStarterMu.Lock()
	jobStarter = s
	jobStarterMu.Unlock()
}

// getJobStarter copies the current JobStarter out under RLock and returns
// it, so callers never hold the lock while calling into the interface (see
// bgbash.go's jobNotifierMu doc comment for why that matters).
func getJobStarter() JobStarter {
	jobStarterMu.RLock()
	defer jobStarterMu.RUnlock()
	return jobStarter
}

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

	// toolsOverride, maxIterationsCap and scoutMode are never populated
	// from JSON — there is no wire field for any of them, and parseTasks
	// never sets them. They exist purely so tools/scout.go can hand
	// runSingleTask a fixed tool profile, iteration cap and "this is a
	// scout" marker without duplicating runSingleTask's
	// model/temperature/fallback resolution or its worktree/history/todo
	// plumbing. Zero value (nil / 0 / false) for every ordinary subagent
	// task leaves opts exactly as it was before scout existed.
	toolsOverride    []string
	maxIterationsCap int
	// fallbacks, when set, replaces the named agent's fallback list. Used by
	// workflow roles (TaskSpec.Fallbacks). Nil keeps the agent default.
	fallbacks []string
	// scoutMode, when true, tells main.go's agentRunner.run to build this
	// child's schema and runtime gate from scoutGate/scoutSchemaJSONForDepth
	// (tools/scout.go) instead of the ordinary AllowOnlySubagent/
	// GetSubagentToolsSchemaJSONForAtDepth path — the latter always folds
	// alwaysAllowedTools ("lua") in, which a scout must never have (see
	// scoutToolProfile's doc comment).
	scoutMode bool
	// systemPrompt, when set, replaces the named agent's system prompt. It is
	// Go-only like the fields above: no json tag, never set by parseTasks.
	// RunSubagentTask (subagent_task.go) is its only writer.
	systemPrompt string
	// softLimit/hardLimit override the agent definition's context limits
	// (flow role). Go-only, like systemPrompt.
	softLimit int
	hardLimit int
	effort    string
}

// subagentResult holds the outcome of one subagent execution.
type subagentResult struct {
	// limitErr is ErrSubagentTruncated or ErrSubagentTimedOut when the run
	// stopped on a limit, nil otherwise. runSubagentTask reports it as an
	// error for tasks with RoleLimits; other callers do not see it.
	limitErr  error
	Task      string       `json:"task"`
	Success   bool         `json:"success"`
	Content   string       `json:"content,omitempty"`
	Thinking  string       `json:"thinking,omitempty"`
	Error     string       `json:"error,omitempty"`
	ToolCalls int          `json:"tool_calls"`
	Usage     stream.Usage `json:"usage"`
	Turns     int          `json:"turns,omitempty"`
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
	turns     int
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
	c.usage.Add(usage)
	c.turns++
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
		Turns:     c.turns,
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
	EventSink
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
	lineMu  sync.Mutex      // protects lineBuf only; collector.mu guards accumulated text
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

	// live, when non-nil, records every Text/Thinking/tool event so the TUI's
	// agent view can show this child's conversation while it runs. Nil for a
	// child without a job id. See transcript_live.go.
	live *liveTranscript
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
	s.live.add(LiveEvent{Kind: "text", Content: text})
	s.touchActivity()
}

func (s *streamingCollector) Thinking(text string) {
	s.collector.Thinking(text)
	s.pushText(text)
	s.live.add(LiveEvent{Kind: "thinking", Content: text})
	s.touchActivity()
}

func (s *streamingCollector) ToolCallStart(name string) {
	s.collector.ToolCallStart(name)
	s.live.add(LiveEvent{Kind: "tool-start", ToolName: name})
	s.touchActivity()
}

func (s *streamingCollector) ToolCallDelta(delta string) {
	s.collector.ToolCallDelta(delta)
	s.live.add(LiveEvent{Kind: "tool-delta", Content: delta})
	s.touchActivity()
}

func (s *streamingCollector) ToolCallEnd(name, result string) {
	s.collector.ToolCallEnd(name, result)
	s.live.add(LiveEvent{Kind: "tool-end", Content: result})
	s.touchActivity()
}

// Compaction records an in-loop compaction in the live transcript, so the
// agent view of this subagent shows its divider (see agent.compactionSink).
func (s *streamingCollector) Compaction(meta session.CompactMeta) {
	s.live.add(LiveEvent{Kind: "compaction", Compaction: meta})
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
	s.lineMu.Lock()
	defer s.lineMu.Unlock()

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
	s.lineMu.Lock()
	defer s.lineMu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.String()
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
		return validationResult(err.Error())
	}
	if len(tasks) == 0 {
		return validationResult("no tasks provided")
	}

	if async, mixed := asyncTaskMode(tasks); mixed {
		return validationResult("cannot mix async and non-async tasks in the same call; issue separate subagent calls")
	} else if async {
		return t.runAsync(ctx, tasks)
	}

	// A blocking call waits for every child. The dispatcher waits for the call
	// too (noDeadline, see timeoutFor), so the call never moves to the
	// background. A child can only be stopped: by kill_job, or when ctx ends.
	//
	// Children always run as jobs when a job registry is wired, so the ids
	// work for report_progress and kill_job. Without a registry (tests only)
	// the tasks run inline.
	if getJobStarter() != nil {
		return t.runBlocking(ctx, tasks)
	}

	results := runTasks(ctx, t.Runner, tasks)
	return resultsToToolResult(results)
}

// resultsToToolResult converts finished subagent results into the ToolResult
// shape the model sees: a single task collapses to plain text with its
// Truncated flag propagated (unchanged from before batching existed), a
// batch becomes a JSON array. Shared by the plain runTasks path and by
// runBlocking, so the two cannot drift apart.
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
			return nil, fmt.Errorf(`tasks array is empty; provide a single task, e.g. {"task":"..."}, or a non-empty tasks array, e.g. {"tasks":[{"task":"..."}]}`)
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
// handed is true only for an async child: its parent got the job ids and
// no result, so finish reports that the parent must be notified.
type spawnedTask struct {
	task  subagentTask
	jobID string
	label string
	done  chan struct{}

	// cancel ends this child's own detached context (jobCtx in spawn) with
	// ErrCancelledByParent as the cause. The job's closure already defers a
	// call to it on normal completion. cancelRemaining calls it early, which
	// is safe: the function is idempotent.
	cancel context.CancelFunc

	mu       sync.Mutex
	finished bool
	handed   bool
	res      subagentResult

	// stopStream is flipped when the task is cancelled outright, so the child
	// stops painting into a tool block that has closed.
	stopStream *atomic.Bool
}

// finish records the result and reports whether the parent must be notified,
// i.e. whether this task was started as async.
func (s *spawnedTask) finish(res subagentResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res = res
	s.finished = true
	return s.handed
}

// claimForCancel reports whether this task is still running, so that it is
// worth cancelling. It is false once finished, under the same lock as finish.
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

const subagentNoticePreviewLimit = 800

func subagentCompletionNotice(label, jobID string, res subagentResult) string {
	status := "finished"
	if !res.Success {
		status = "FAILED"
	}
	preview := strings.TrimSpace(res.Content)
	if preview == "" {
		preview = strings.TrimSpace(res.Error)
	}
	if len([]rune(preview)) > subagentNoticePreviewLimit {
		runes := []rune(preview)
		preview = string(runes[:subagentNoticePreviewLimit]) + "…"
	}
	if preview == "" {
		preview = "(no summary returned)"
	}
	return fmt.Sprintf("[subagent] %q %s (job_id=%q). Short result: %s. Tell the user the subagent has finished, then use wait(job_id=%q) for the full result if needed.", label, status, jobID, preview, jobID)
}

// spawn starts one child as a job and returns at once.
//
// handedAtStart is true for an async call, where the parent is told the ids
// and gets a notice when the child finishes. It is false for a blocking call,
// which waits for the child itself (see runBlocking).
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
	jobCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	st.cancel = func() { cancel(ErrCancelledByParent) }
	stopStream := &atomic.Bool{}
	st.stopStream = stopStream

	parentID, _ := ctx.Value(JobIDCtxKey{}).(string)
	// Copy the interface value out under RLock once, then call into it
	// unlocked (see getJobStarter's doc comment). spawn is only reached after
	// the caller has confirmed a starter is wired, so this is never nil.
	starter := getJobStarter()
	job := starter.Start(jobCtx, task.Task, JobKindSubagent, parentID, func(runCtx context.Context, jobID string) (string, bool, error) {
		defer cancel(nil)
		defer close(st.done)
		runCtx = context.WithValue(runCtx, JobIDCtxKey{}, jobID)
		runCtx = context.WithValue(runCtx, streamStopCtxKey{}, stopStream)
		res := runSingleTask(runCtx, t.Runner, task, 0, streamToParent)

		// Tell the parent it finished, but only for an async child. A blocking
		// call gets its result inline, so a notice about it would be noise.
		if st.finish(res) {
			notifyToParent(parentID, subagentCompletionNotice(st.label, jobID, res))
		}

		if !res.Success {
			return res.Content, res.Truncated, errors.New(res.Error)
		}
		return res.Content, res.Truncated, nil
	})
	BindCallJob(ctx, job.ID())
	st.jobID = job.ID()
	return st
}

func (t *SubagentTool) runAsync(ctx context.Context, tasks []subagentTask) ToolResult {
	if getJobStarter() == nil {
		return ToolResult{Type: "result", Success: false, Error: "async subagent spawn unavailable: job registry not configured"}
	}

	spawned := make([]*spawnedTask, 0, len(tasks))
	for _, task := range tasks {
		// streamToParent is false: by the time an async child produces output
		// the parent's tool call has returned and its block is closed. The
		// result lives in the job registry and is read with wait().
		spawned = append(spawned, t.spawn(ctx, task, true, false))
	}
	return ToolResult{Type: "result", Success: true, Content: spawnedJobsMessage(spawned)}
}

// spawnedJobsMessage is what the parent reads after an async call starts its
// children: the job ids as JSON, then what the notices mean.
//
// The ids alone are not enough. A parent handed a bare list has no way to know
// that a child can block on a question and needs an answer, and a blocked
// child that nobody answers burns its whole wall-clock limit and then discards
// everything it did.
func spawnedJobsMessage(spawned []*spawnedTask) string {
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
	fmt.Fprintf(&b, `

%d task(s) are now running in the background. Get on with work that does not depend on them — and if there is a person waiting, talk to them: the turn is yours again.

If a child becomes BLOCKED on a question, a notice names its job_id and the question. That child waits for an answer right now. Relay the question to the user, or give information you genuinely know; never invent an answer for a human who has not replied. Call answer_job(job_id=..., text="...") for that id. A later notice naming the same job_id and question is the SAME question restated, not a second one — do not answer it twice.

You will also be told when a child finishes. Then read the result with wait(job_id=...). Nothing else delivers it.

Do not call wait before you are told: it can only say "still running", which the next notice would have told you for free.`, len(spawned))
	return b.String()
}

// runBlocking runs a blocking subagent call as background jobs and waits for
// all of them. Each child is a job, so report_progress and kill_job work on
// it. The call returns when every child is terminal, or when ctx ends. Then
// the children that still run are cancelled (see cancelRemaining).
//
// A blocking call never hands a child to the background. The dispatcher
// waits for this call (timeoutFor gives subagent noDeadline), so the call
// ends only when its children are done or its ctx is cancelled.
func (t *SubagentTool) runBlocking(ctx context.Context, tasks []subagentTask) ToolResult {
	spawned := make([]*spawnedTask, 0, len(tasks))
	for _, task := range tasks {
		// streamToParent is true: the parent's tool block is open, and the live
		// output is the only sign of progress.
		spawned = append(spawned, t.spawn(ctx, task, false, true))
	}

	for _, st := range spawned {
		select {
		case <-st.done:
		case <-ctx.Done():
			return t.cancelRemaining(spawned)
		}
	}

	// st.result() is safe here: <-st.done fires only after finish() stored the
	// result (spawn's closure calls finish before its deferred close(done)).
	results := make([]subagentResult, len(spawned))
	for i, st := range spawned {
		results[i] = st.result()
	}
	return resultsToToolResult(results)
}

// cancelRemaining is called by runBlocking when its ctx ends. Every child not
// yet finished is cancelled with ErrCancelledByParent, so that its result is
// not reported as a kill_job stop. The call returns at once with the finished
// results plus an error about the rest.
//
// claimForCancel gives the exactly-once check against a concurrent finish().
// stopStream is flipped too, so a cancelled child stops painting into a tool
// block that this call is about to return from.
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
		SoftLimit:        def.CompactSoftLimit,
		HardLimit:        def.CompactHardLimit,
		Fallbacks:        def.Fallback,
		SystemPromptMode: def.SystemPromptMode,
	}
	if task.softLimit > 0 {
		opts.SoftLimit = task.softLimit
	}
	opts.Effort = task.effort
	if task.hardLimit > 0 {
		opts.HardLimit = task.hardLimit
	}
	if task.InheritHistory {
		opts.History = connector.ConversationFromContext(ctx)
	}
	// toolsOverride/maxIterationsCap/scoutMode are scout.go's private hook
	// into this function (see subagentTask's doc comment) — nil/0/false
	// for every ordinary subagent task, so this changes nothing for the
	// plain-subagent path.
	if task.toolsOverride != nil {
		opts.Tools = task.toolsOverride
	}
	if task.fallbacks != nil {
		opts.Fallbacks = task.fallbacks
	}
	if task.maxIterationsCap > 0 {
		opts.MaxIterationsCap = task.maxIterationsCap
	}
	if task.scoutMode {
		opts.ScoutMode = true
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
		// Not forwarded to the parent, but the tokens still show that the
		// job is alive, so the job id is kept (see streamingCollector.jobID).
		jobID, _ := ctx.Value(JobIDCtxKey{}).(string)
		c = &streamingCollector{collector: newCollector(), jobID: jobID}
	}
	if jobID, _ := ctx.Value(JobIDCtxKey{}).(string); jobID != "" {
		c.live = startLiveTranscript(jobID)
		c.live.add(LiveEvent{Kind: "prompt", Content: task.Task})
	}

	// Hand the runner our forwarding Sink via context (see SubagentSink's
	// doc comment) so it drives agent.Run with this collector directly,
	// instead of building its own non-forwarding one — that was the actual
	// bug behind the subagent modal staying blank while running: c existed
	// but nothing ever called its Text/Thinking.
	runCtx = context.WithValue(runCtx, SubagentSinkCtxKey{}, c)

	// This child's own nesting depth is the caller's plus one — top level
	// (nothing set on ctx) is depth 0, so its subagent's own children land
	// at depth 1, a scout that subagent spawns lands at depth 2, and so on.
	// See DepthCtxKey's doc comment for how this bounds which of
	// subagent/scout (if either) this child may itself call.
	runCtx = WithDepth(runCtx, DepthFromContext(ctx)+1)

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

	// Stamp this child's own identity as the caller identity scout.go's
	// concurrency semaphore keys on (see scoutCallerCtxKey in scout.go).
	// Reuses todoAgentID rather than minting a second fresh id: it is
	// already exactly "a fresh, unique id for this one child", and using
	// JobIDCtxKey instead would collapse every scout nested under a job
	// (or under another scout, which never carries a job id at all — see
	// scout.go's doc comment on why it strips JobIDCtxKey) into one shared
	// bucket instead of one per actual caller.
	runCtx = context.WithValue(runCtx, scoutCallerCtxKey{}, todoAgentID)

	// Every tool resolves relative paths against Workdir (see workdir.go), so
	// this one line is what makes the child's reads, writes and shell commands
	// land in its own checkout.
	if wt != nil {
		runCtx = WithWorkdir(runCtx, wt.Dir)
	}

	// Run the task via the runner interface. def.SystemPrompt is empty when
	// no agent was named (def is the zero value), so this also covers the
	// plain-subagent case without a separate branch on task.Agent.
	system := def.SystemPrompt
	if task.systemPrompt != "" {
		system = task.systemPrompt
	}
	var content string
	var err error
	if system != "" {
		content, err = runner.RunTaskWithSystem(runCtx, task.Task, mName, system, opts)
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
		res.limitErr = roleLimitCause(err)
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
			res.limitErr = ErrSubagentTimedOut
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

// roleLimitCause returns the limit sentinel of a run that stopped on its
// iteration cap or its deadline, or nil for any other error.
func roleLimitCause(err error) error {
	switch {
	case errors.Is(err, ErrSubagentTruncated):
		return ErrSubagentTruncated
	case errors.Is(err, ErrSubagentTimedOut):
		return ErrSubagentTimedOut
	}
	return nil
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
