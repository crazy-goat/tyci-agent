package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/decodo/tyci/internal/hooks"
	"github.com/decodo/tyci/locks"
)

// SubagentOptions are per-call knobs the parent supplies for a single
// subagent invocation. Built per-call in main.go and tools/subagent.go;
// not serialized, so future fields are added as Go struct additions.
type SubagentOptions struct {
	// MaxIterations caps the child agent's tool-call turns. Semantics
	// (mirrors agent.Options.MaxIterations):
	//   - nil       → use ResolveMaxIter's default (currently unlimited)
	//   - 0 or <0   → unlimited
	//   - >0        → cap the child at that many turns
	// Note: 0 is *not* "no turns allowed" — it's "use the unlimited
	// default". A parent that wants to forbid tool calls entirely must
	// avoid invoking the subagent at all (or use a child without tools).
	MaxIterations *int

	// Tools, when non-empty, restricts the child to these tool names.
	// Empty/nil means "every tool except subagent" (today's behavior).
	Tools []string

	// Temperature, when non-nil, is the sampling temperature for the child.
	// Pointer because 0 is meaningful ("deterministic"), not "unset".
	Temperature *float64

	// MaxTokens caps the child's reply length. 0 means unset — the
	// connector's default applies. Comes from the named agent definition's
	// `max_tokens` frontmatter.
	MaxTokens int

	// Fallbacks are "provider/model" specs, NOT resolved clients: the tools
	// package is a leaf and must not reach into the provider catalog. The
	// composition root (main.go) resolves them, exactly as it already does
	// for the top-level agent — see agent.Config.Fallbacks.
	Fallbacks []string

	// SystemPromptMode mirrors agentdefs.Def.SystemPromptMode ("append" or
	// "replace"). The tools package does not build prompts — it only carries
	// the mode to the composition root, which owns providers.
	SystemPromptMode string
}

// DefaultSubagentMaxIterations is the cap applied when SubagentOptions has
// no explicit MaxIterations. -1 means unlimited. Defined as a constant (not
// a function) so both main.go and tests see the same value.
//
// This is a deliberate behavior change from the hard-coded 10 that preceded
// it: a caller that omits MaxIterations now runs unbounded, held only by
// SubagentOptions' semantics and the wall-clock backstop in subagent.go. A
// caller that wants a finite cap has to pass an explicit positive integer.
const DefaultSubagentMaxIterations = -1

// TruncatedMarker is the literal suffix appended to a single-task subagent
// result's content when the result is flagged as truncated, so the parent
// LLM has a stable, parseable token (in addition to the inline [note: ...]
// prose). The parallel-array path encodes truncation per-item via
// json.Marshal; single-task has no such structural path because the agent
// runner turns tool results into a `(string, error)` at the package
// boundary, so this marker is the only way to surface the flag. Exported
// because the package-main caller (cmd_interactive.go toolsAdapter) needs
// to use the same literal.
const TruncatedMarker = "[truncated=true]"

// ResolveMaxIter converts SubagentOptions into the concrete cap to pass to
// agent.Options.MaxIterations. Separated from main.go so it can be table-
// tested in tools/subagent_test.go.
func ResolveMaxIter(opts SubagentOptions) int {
	if opts.MaxIterations == nil {
		return DefaultSubagentMaxIterations
	}
	v := *opts.MaxIterations
	if v <= 0 {
		return DefaultSubagentMaxIterations
	}
	return v
}

// SubAgentRunner is the interface that tools/subagent.go uses to run
// agent tasks without importing the agent package directly.
// This keeps the tools package as a leaf layer with no upward dependencies.
type SubAgentRunner interface {
	// RunTask executes a single agent task and returns the result text.
	RunTask(ctx context.Context, task string, model string, opts SubagentOptions) (string, error)

	// RunTaskWithSystem executes a single agent task with a custom system prompt.
	RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts SubagentOptions) (string, error)
}

// ToolResult is the outcome of a single tool execution, returned to the
// calling LLM as a JSON-encoded tool message.
type ToolResult struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Content string `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
	// Truncated is true when the tool ran to completion but the result is
	// known to be incomplete (e.g. a subagent hit its MaxIterations cap and
	// the child self-stopped with a partial answer). The content is still
	// usable, but callers and parents should treat it with reduced
	// confidence. Distinct from Success=false.
	Truncated bool `json:"truncated,omitempty"`
}

type Tool interface {
	Name() string
	Run(ctx context.Context, input map[string]any) ToolResult
}

func GetToolsSchema() []map[string]any {
	return []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "find",
				"description": "Find files by glob (method=\"glob\", e.g. **/*.go) or search their contents (method=\"grep\": text, word or regex). Returns relative paths. Respects .gitignore, skips binaries, and says when a result cap was hit. Use output=\"files\" or \"count\" while deciding where to look, and \"lines\" only when you need to read the hits — over many matches that is the difference between a list of paths and a wall of code in your context.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"method":        map[string]any{"type": "string", "enum": []string{"glob", "grep"}, "description": "Search method: \"glob\" to find files by path pattern, \"grep\" to search file contents (default: \"glob\")"},
						"pattern":       map[string]any{"description": "Glob pattern (method: glob) or text/regex (method: grep)"},
						"cwd":           map[string]any{"type": "string", "description": "Base directory (default: .)"},
						"include":       map[string]any{"description": "Grep only — glob pattern or array to include (default: **/*)"},
						"exclude":       map[string]any{"description": "Glob pattern or array to exclude (default: .git/**, node_modules/**, etc.)"},
						"mode":          map[string]any{"type": "string", "enum": []string{"text", "regex", "word"}, "description": "Grep only — search mode (default: text)"},
						"caseSensitive": map[string]any{"type": "boolean", "description": "Grep only — case-sensitive search (default: true)"},
						"context":       map[string]any{"type": "integer", "description": "Grep only — lines of context before/after each match (default: 0)"},
						"limit":         map[string]any{"type": "integer", "description": "Max results (default: 500 for glob, 100 for grep)"},
						"output":        map[string]any{"type": "string", "enum": []string{"lines", "files", "count"}, "description": "Grep only — output format (default: lines)"},
						"maxLineLength": map[string]any{"type": "integer", "description": "Grep only — trim long lines (default: 300)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "todo",
				"description": "The run's plan, and a gate: every other tool is refused until at least one item exists. todo(action=\"add_batch\", items=[...]) creates the whole plan in one call. Keep items at the granularity of something you can finish and verify. Mark done when done and blocked (with a reason) when it cannot proceed — the turn will not end quietly with items still open. actions: add/add_batch/update/doing/blocked/done/remove/list/clear.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":   map[string]any{"type": "string", "enum": []string{"add", "add_batch", "update", "doing", "blocked", "done", "remove", "list", "clear"}},
						"id":       map[string]any{"type": "integer", "description": "Todo id for update/done/remove"},
						"content":  map[string]any{"type": "string", "description": "Todo text (used by add)"},
						"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}, "description": "Default: todo"},
						"parentId": map[string]any{"type": "integer", "description": "Optional parent todo id"},
						"items": map[string]any{
							"type":        "array",
							"description": "add_batch only — array of todo entries to create. Each: {content: string (required), status?: 'todo'|'doing'|'done'|'blocked', parentId?: number}. Example: [{content:\"Write tests\"}, {content:\"Fix bug\", status:\"doing\"}]",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"content":  map[string]any{"type": "string", "description": "Todo text"},
									"status":   map[string]any{"type": "string", "enum": []string{"todo", "doing", "done", "blocked"}, "description": "Default: todo"},
									"parentId": map[string]any{"type": "integer", "description": "Optional parent todo id"},
								},
								"required": []string{"content"},
							},
						},
					},
					"required": []string{"action"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "read",
				"description": "Read a file. offset/limit for a range, lineNumbers=true when you need exact line numbers. Reading three or more files means a lua script instead: what a script reads is discarded, what you read stays in this conversation for the rest of the session.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":        map[string]any{"type": "string", "description": "File path to read"},
						"offset":      map[string]any{"type": "integer", "description": "Start line, 1-indexed"},
						"limit":       map[string]any{"type": "integer", "description": "Maximum lines to read"},
						"lineNumbers": map[string]any{"type": "boolean", "description": "Prefix lines as N| text (default: false)"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "write",
				"description": "Create or overwrite a file (content, optional range) or replace exact text (oldString+newString, which triggers edit mode). Modifying a file that already exists requires having read it first, unchanged since — a refused write means read it again and redo the edit against what it says now, never retry the same call. oldString must match exactly once unless occurrence says otherwise; include enough surrounding text to be unique. New files and range=\"append\" need no prior read. dryRun=true previews.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":       map[string]any{"type": "string", "description": "File path to write"},
						"content":    map[string]any{"type": "string", "description": "Content to write (write mode; omit when using oldString)"},
						"range":      map[string]any{"description": "Write mode only: line number, 'from...to', 'before:N', 'after:N', 'all', or -1/'append'"},
						"oldString":  map[string]any{"type": "string", "description": "Exact text to replace (edit mode; triggers edit when present)"},
						"newString":  map[string]any{"type": "string", "description": "Replacement text (edit mode; required with oldString)"},
						"occurrence": map[string]any{"description": "Edit mode only: occurrence number or 'all'. Default requires exactly one match"},
						"dryRun":     map[string]any{"type": "boolean", "description": "Edit mode only: preview without writing (default: false)"},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "bash",
				"description": "Run a shell command; use it only when no other tool fits — find, read and write are cheaper and bounded. It blocks, and after 30s the command is moved to the background and you are NOTIFIED when it finishes: do other work, and never re-run a backgrounded command, because a second copy races the first. run_in_background=true for work you already know is long; background_after=0 to stay blocked; timeout (default 120s) is the total limit, not a promise to block.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description":       map[string]any{"type": "string", "description": "Short description of what this command does. Also used as the label for a background job, so keep it recognisable."},
						"command":           map[string]any{"type": "string", "description": "Command to execute"},
						"timeout":           map[string]any{"type": "integer", "description": "How long the command may run in total, in seconds (default: 120). This is a limit, not a promise to block: the command still moves to the background after 30s and keeps running, and you can then wait(job_id=...) on it. To stay in the foreground instead, set background_after=0."},
						"run_in_background": map[string]any{"type": "boolean", "description": "Start the command in the background immediately and return a job_id without waiting for any output. Use for long builds, test suites or watchers when you have other work to get on with."},
						"background_after":  map[string]any{"type": "integer", "description": "Seconds to wait before moving the command to the background (default: 30). 0 disables the move, so the command runs in the foreground until it finishes or hits its timeout."},
					},
					"required": []string{"command"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "lua",
				"description": "Run a Lua script that calls other tools: tool(name, args) returns {success, content, error}. Use it for any loop over three or more items, or any step that depends on what the last one returned — one round trip for the whole loop, and what the script reads never enters your context. log() reports progress live; return hands back the answer (a table becomes JSON); args carries input data in. Check res.success on every call — a script that ignores a failure returns a confident wrong answer. Return the CONCLUSION, not the material. Fan out from inside a script: build the task list, then tool(\"subagent\", {tasks = ...}). Sandboxed to the pure language: no io, os or require. Aborted at its timeout and after 500 tool calls, so make loops terminate.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string", "description": "A few words on what this script does, e.g. \"rename oldName across the Go files\". Shown to the person watching, who cannot read the script at a glance — without it every script in the transcript looks the same."},
						"script":      map[string]any{"type": "string", "description": "Lua source. Use return to hand back the result, log() to report progress along the way."},
						"args":        map[string]any{"type": "object", "description": "Optional values made available to the script as the global table 'args'. Use it to keep data out of the source text."},
						"timeout":     map[string]any{"type": "integer", "description": fmt.Sprintf("Wall-clock limit in seconds (default %d, max %d). The script is aborted when it expires; work it already did is not undone.", LuaDefaultTimeoutSec, LuaMaxTimeoutSec)},
					},
					"required": []string{"script"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "subagent",
				"description": "Delegate to child agents, each with its own context window. Working through MANY agents is the intended mode here, not the exception: a child reads into its own window and hands you back only its conclusion. Delegate when answering means reading more than about three files, when the work is a search, survey or review (you want the finding, not the material), and ALWAYS when you have two or more independent pieces of work — ONE call with tasks=[...], run in parallel, costing about as much wall-clock as the slowest one. A call without async blocks — but only for 60s: after that the children carry on in the background, you are notified as usual, and the turn ends so the person at the keyboard gets their prompt back. Set async=true whenever you do not need the result this turn: you get job_ids back immediately and are NOTIFIED when each finishes or blocks on a question, so get on with other work; wait(job_id) reads a result, answer(job_id, text) unblocks a question and is urgent — a blocked child makes no progress and its work is discarded when it times out. Do not delegate single-file edits, work needing the exact bytes, or anything that depends on this conversation: the child sees ONLY your task text, no history and no earlier findings, so state what to do, which paths, and what to return, in that order. Children have every tool except subagent, including lock/unlock — tell parallel tasks to lock the paths they write, or better, give each one isolation=\"worktree\" so they write in separate checkouts and no locking is needed. Bound anything that might wander with max_iterations. agent=\"name\" runs it under a definition from .tyci/agents (call the agents tool for the current list).",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"task":  map[string]any{"type": "string", "description": "Clear, detailed task description for the child agent. Write it like a prompt: explain what to do, what files to read/write, what to return. The child has read/write/bash tools."},
						"agent": map[string]any{"type": "string", "description": "Named agent to use. Definitions live in ./.tyci/agents/<name>.md (project) or ~/.tyci/agents/<name>.md (global), project winning; each supplies the child's system prompt and may pin its model, max_iterations and allowed tools. A list is injected into your system prompt at session start, but it goes stale if a definition is added or edited mid-session — call the \"agents\" tool for a fresh list or to see one definition's full details. An unknown name is an error, not a fallback."},
						"tasks": map[string]any{"type": "array", "description": "Array of parallel tasks to run concurrently", "items": map[string]any{"type": "object", "properties": map[string]any{
							"task":           map[string]any{"type": "string", "description": "Clear task description for this parallel subtask, including what to return. The child has every tool except subagent itself."},
							"agent":          map[string]any{"type": "string", "description": "Named agent to use"},
							"model":          map[string]any{"type": "string", "description": "Optional model override (format: provider/model)"},
							"max_iterations": map[string]any{"type": "integer", "description": "Cap this child's tool-call turns. Set a positive integer to bound a risky subtask (e.g. exploration, code review); omit to use the runner default (currently unlimited, bounded by a 600s wall-clock timeout). 0 and negative values mean unlimited."},
							"async":          map[string]any{"type": "boolean", "description": "Run this task as a background job and return its job_id immediately instead of blocking. Must match every other task's async value in the same call."},
							"isolation":      map[string]any{"type": "string", "enum": []string{"worktree"}, "description": "Give this child its own checkout of the repository, on its own branch, instead of the shared working directory: \"worktree\". Use it whenever two or more children WRITE at the same time — then they cannot clobber each other and nothing has to take turns on a lock. The cost is that its edits are not in your tree: the result tells you the branch and how to diff it, and you decide whether to merge. A child that only reads needs nothing here, and its checkout is removed automatically when it changed no files. Needs a git repository."},
						}, "required": []string{"task"}}},
						"model":          map[string]any{"type": "string", "description": "Optional model override for single task (format: provider/model, e.g. opencode-zen/big-pickle)"},
						"max_iterations": map[string]any{"type": "integer", "description": "Cap on the child's tool-call turns. Omit or 0 to use the runner's default (currently unlimited); negative = unlimited. Useful for bounding long-running subtasks like exploration or code review."},
						"async":          map[string]any{"type": "boolean", "description": "Run as a background job and return a job_id immediately instead of blocking until it finishes. Poll with wait(job_id=...)."},
						"isolation":      map[string]any{"type": "string", "enum": []string{"worktree"}, "description": "Give this child its own checkout of the repository, on its own branch, instead of the shared working directory: \"worktree\". Use it whenever two or more children WRITE at the same time — then they cannot clobber each other and nothing has to take turns on a lock. The cost is that its edits are not in your tree: the result tells you the branch and how to diff it, and you decide whether to merge. A child that only reads needs nothing here, and its checkout is removed automatically when it changed no files. Needs a git repository."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "wait",
				"description": "Wait for a background job (job_id) or pause deliberately (seconds alone). With a job_id it waits until that job finishes or blocks on a question and returns the result — not a status snapshot — so one call gets you the answer; seconds is optional and defaults to 10 minutes, and the wait ends early if someone types. You do not need it to find out that a job finished, because you are notified; use it when you have nothing else to do, or to read a result once you are told.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"seconds": map[string]any{"type": "integer", "description": fmt.Sprintf("How long to wait, in seconds. Clamped to [%d, %d].", MinWaitSeconds, MaxWaitSeconds)},
						"job_id":  map[string]any{"type": "string", "description": "Id of a background job. The call waits until that job actually finishes (or blocks on a question), so it returns the result rather than a status — seconds is optional here and defaults to 10 minutes. It ends early if someone types. Omit for a plain sleep."},
						"note":    map[string]any{"type": "string", "description": "Optional note describing what you're waiting for, echoed back for context."},
					},
					"required": []string{"seconds"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "kill_job",
				"description": "Kill a backgrounded shell command and everything it spawned — wrong command, no longer needed, or stuck. Its output so far stays readable with wait(job_id). Shell jobs only, not async subagents.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{"type": "string", "description": "Id of the background command to stop, as returned by the bash tool."},
					},
					"required": []string{"job_id"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "help",
				"description": "Full documentation for a tool, with worked examples. The tool list in your prompt is one line each on purpose; this is the manual. help() lists every tool, help(tool=\"lua\") returns one. Read help(\"lua\") and help(\"subagent\") before first using them, and whenever a refusal or an error surprises you — guessing at a tool costs more than asking.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"tool": map[string]any{"type": "string", "description": "Tool name. Omit to list every available tool."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "memory",
				"description": "Project notes that survive the session: every note in .tyci/memory/ is loaded into the next session's system prompt, so writing one is how something you worked out stops having to be worked out again. Write when you learn something NOT obvious from the code — the command that really runs the tests, a rule the compiler does not enforce, a decision and its reason, a trap you already fell into. Never for what the code plainly says, for one-off task detail, or for this conversation: you pay for every note on every future request. Say WHY, keep it to a few sentences, rewrite under the same name to correct it, delete what stopped being true. actions: list (default), read, write, delete.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":  map[string]any{"type": "string", "description": "list (default), read, write or delete"},
						"name":    map[string]any{"type": "string", "description": "Short slug identifying the note, e.g. \"test-command\". Required for read/write/delete. Writing an existing name replaces it."},
						"content": map[string]any{"type": "string", "description": "The note itself (write only). A few sentences, including why it matters."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "cron",
				"description": "Prompts that run later, without you or anybody else being there. This is the only way \"check again in an hour\" or \"every morning before I start\" survives: the session ends and everything it intended ends with it. A run is a FRESH agent given only the saved prompt — no history, no findings from here — so write the prompt to stand alone: what to do, where, and what counts as done. Schedules are \"every 30m\"/\"every 6h\" or \"at 07:30\" (local, daily); a new job is due at once so you find out straight away whether it works. Output goes to a per-job log, read with action=\"logs\". Use it for what repeats or must happen later — a nightly test run, a periodic check on a queue, a morning summary. Do NOT use it as a reminder to yourself inside this conversation, and do not schedule anything the person did not ask to be repeated. You are notified when a scheduled run finishes, so never poll. The schedule only advances while an interactive session is open — say that instead of promising a job will fire. actions: list (default), add, remove, enable, disable, logs, run_now.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"action":   map[string]any{"type": "string", "enum": []string{"list", "add", "remove", "enable", "disable", "logs", "run_now"}, "description": "What to do. Defaults to list."},
						"name":     map[string]any{"type": "string", "description": "Short slug identifying the job (letters, digits, - and _). Required for everything except list."},
						"prompt":   map[string]any{"type": "string", "description": "What the scheduled agent is asked to do. It gets NOTHING else: no conversation history, no earlier findings. State the task, the paths, and what to report."},
						"schedule": map[string]any{"type": "string", "description": "When to run: \"every 30m\", \"every 6h\" (shortest interval is 1m, measured from the end of the last run) or \"at 07:30\" (local time, once a day)."},
						"dir":      map[string]any{"type": "string", "description": "Directory to run in. Defaults to the current one, recorded now — so the job keeps meaning the same project later."},
						"model":    map[string]any{"type": "string", "description": "Optional model override (format: provider/model). Omit to use the configured default; a cheap model is usually right for a recurring check."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "skills",
				"description": "List available skills (no arguments) or load one's full content (name). Skills are markdown in ~/.tyci/skills/<name>/SKILL.md.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Name of the skill to load (directory name). If omitted, lists all available skills."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "agents",
				"description": "List the named agents usable as subagent(agent=\"name\"), or load one definition in full. Call it rather than trusting the list in your system prompt, which goes stale as soon as a definition is added or edited mid-session. An unknown name is an error, not a fallback.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string", "description": "Name of the agent to load details for. If omitted, lists all available agents."},
					},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "lock",
				"description": "Advisory-lock a path so parallel agents keep off it. Cooperative only: it stops nobody physically, it tells them. Returns a holder id — keep it, unlock needs it. Omit seconds for a session-lifetime lock. Anything you write while other agents run belongs behind one of these.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "File or directory path to lock"},
						"seconds": map[string]any{"type": "integer", "description": "Optional TTL in seconds. Omit for a lock that lasts until explicit unlock or session end."},
					},
					"required": []string{"path"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "unlock",
				"description": "Release a lock, using the holder id that lock returned. Unlock as soon as you are done with the path — another agent may be waiting on it.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "File or directory path to unlock"},
						"holder": map[string]any{"type": "string", "description": "The holder id returned by the earlier \"lock\" call"},
					},
					"required": []string{"path", "holder"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "ask",
				"description": "Ask the parent one question and BLOCK until it answers. Only usable inside a job (any subagent call, or a /btw side-conversation). If there is no way for an answer to ever reach this call, it fails immediately instead of waiting out its timeout for nothing. A last resort for genuine ambiguity: you make zero progress while waiting, and if the wall clock runs out first your whole run is discarded. Prefer stating an assumption and proceeding.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string", "description": "The question to ask, phrased so a one-shot text reply can answer it."},
					},
					"required": []string{"question"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "answer",
				"description": "Answer a job that is blocked on ask. Urgent the moment you are told about one: the job makes no progress meanwhile and everything it has done is discarded if it times out unanswered.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{"type": "string", "description": "The id of the job currently waiting for an answer."},
						"text":   map[string]any{"type": "string", "description": "The answer text to deliver back to that job's pending \"ask\" call."},
					},
					"required": []string{"job_id", "text"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "report_progress",
				"description": "Post a short status note from inside a job (any subagent call, or a /btw side-conversation). Only usable inside a job; it always succeeds and updates the job record. Whether anyone actually reads it before the job finishes depends on how you were spawned: an async job or /btw is watchable live (wait, the jobs panel). A blocking subagent call under `tyci run`/`--print` hands out no job id to read it by and has no jobs panel either, so there the note simply never reaches anyone before the job is already done.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"text": map[string]any{"type": "string", "description": "Short status note describing current progress."},
					},
					"required": []string{"text"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "resume",
				"description": "Continue a FINISHED async job with a new task. It keeps its entire conversation, so a follow-up (\"now also fix the tests\") costs no re-explaining — this is much cheaper than spawning a fresh child and describing the context again. Returns a new job_id, notified like any async spawn. Only works on a job that finished normally.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{"type": "string", "description": "The id of the finished async job to continue."},
						"task":   map[string]any{"type": "string", "description": "The new message to append to that job's conversation before continuing it."},
					},
					"required": []string{"job_id", "task"},
				},
			},
		},
		{
			"type": "function",
			"function": map[string]any{
				"name":        "web",
				"description": "Reach the web. method=\"search\" for real-time results, \"lookup\" for encyclopedic facts (cheaper, and enough for most questions), \"get\" to fetch one URL as markdown or raw.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"method":     map[string]any{"type": "string", "enum": []string{"search", "lookup", "get"}, "description": "search=real-time web search (Exa AI, needs internet); lookup=encyclopedic facts (DuckDuckGo+Wikipedia, works for most knowledge); get=fetch URL"},
						"what":       map[string]any{"type": "string", "description": "Search query, lookup term, or URL to fetch"},
						"numResults": map[string]any{"type": "integer", "description": "search only — number of results (default 8, max 25)"},
						"type":       map[string]any{"type": "string", "enum": []string{"auto", "fast", "deep"}, "description": "search only — Exa search mode (default auto)"},
						"format":     map[string]any{"type": "string", "enum": []string{"markdown", "original"}, "description": "get only — output format (default markdown)"},
					},
					"required": []string{"method", "what"},
				},
			},
		},
	}
}

var toolsSchema json.RawMessage

func init() {
	// Load Lua tools from user directories
	LoadAndRegisterLuaTools()

	data, _ := json.Marshal(GetToolsSchema())
	toolsSchema = data
}

func GetToolsSchemaJSON() json.RawMessage {
	return toolsSchema
}

// GetAllToolsSchema returns all tools including MCP tools.
func GetAllToolsSchema() []map[string]any {
	schema := GetToolsSchema()
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil {
		schema = append(schema, mcpRunner.MCPToolsSchema()...)
	}
	return schema
}

// GetAllToolsSchemaJSON returns all tools schema as JSON including MCP tools.
func GetAllToolsSchemaJSON() json.RawMessage {
	data, _ := json.Marshal(GetAllToolsSchema())
	return data
}

// GetSubagentToolsSchema returns tool definitions excluding subagentDeniedTools
// ("subagent", to prevent recursion, and "agents", whose only purpose is
// discovering names for subagent(agent="name") — a child that cannot spawn
// subagents has nothing to do with that list, so it isn't worth tempting it
// with). See subagentDeniedTools in toolgate.go for the shared list this and
// GetSubagentToolsSchemaJSONFor and main.go's runtime gate all read from.
//
// A connected MCP server's tools ride along unconditionally, appended after
// the filter rather than run through it: subagentDeniedTools is about
// "subagent" and "agents" specifically, and none of a server's own tools
// carry that recursion/discovery risk. This is the unrestricted case only
// (no tools: whitelist) — GetSubagentToolsSchemaJSONFor applies its own,
// narrower rule once a whitelist is present. See mcpAllowedByWildcard's doc
// comment (tools/mcp.go) for why the two cases differ.
func GetSubagentToolsSchema() []map[string]any {
	schema := GetToolsSchema()
	filtered := make([]map[string]any, 0, len(schema))
	for _, s := range schema {
		if fn, ok := s["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && IsSubagentDenied(name) {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil {
		filtered = append(filtered, mcpRunner.MCPToolsSchema()...)
	}
	return filtered
}

// GetSubagentToolsSchemaJSON marshals GetSubagentToolsSchema fresh on every
// call rather than from a package-init snapshot: which MCP servers are
// connected can change over a session's lifetime (see tools.InitMCP), so a
// cached snapshot taken before any server had a chance to connect would
// permanently omit MCP tools from every unrestricted subagent.
func GetSubagentToolsSchemaJSON() json.RawMessage {
	data, _ := json.Marshal(GetSubagentToolsSchema())
	return data
}

// GetSubagentToolsSchemaJSONFor returns the subagent-visible tool schema
// restricted to allowed (a named agent's frontmatter `tools:` list).
// Empty/nil allowed means no restriction — returns
// GetSubagentToolsSchemaJSON() unchanged (every MCP tool included, same as
// today).
//
// Every subagentDeniedTools entry ("subagent", "agents") is dropped even if
// allowed lists it explicitly: recursion into child subagents is never
// permitted, and "agents" exists only to discover names for
// subagent(agent="name"), which a child that cannot call subagent has no
// use for — regardless of what an agent definition's frontmatter says. An
// allowed name that matches no known tool is silently skipped, not an
// error — a typo in an agent's `tools:` line should degrade to "that tool
// is unavailable", not a startup crash.
//
// An MCP tool (mcp_<server>_<tool>) is included only if allowed names it
// explicitly, or names a mcp_<server>_* wildcard for its server — it is
// NOT auto-granted the way alwaysAllowedTools are. A whitelist is a
// deliberate narrowing; every write-capable tool a connected server
// happens to expose should not ride along with it silently just because
// the whitelist couldn't have named a tool it had never seen. See
// mcpAllowedByWildcard (tools/mcp.go) for the matching runtime-gate side —
// AllowOnlySubagent must honour exactly the same two cases so a tool
// offered here is always one the gate will actually let through.
func GetSubagentToolsSchemaJSONFor(allowed []string) json.RawMessage {
	if len(allowed) == 0 {
		return GetSubagentToolsSchemaJSON()
	}
	want := make(map[string]bool, len(allowed)+len(alwaysAllowedTools))
	for _, name := range allowed {
		if IsSubagentDenied(name) {
			continue
		}
		want[name] = true
	}
	// Always present, whatever the definition says. See alwaysAllowedTools.
	for _, name := range alwaysAllowedTools {
		want[name] = true
	}
	schema := GetSubagentToolsSchema()
	filtered := make([]map[string]any, 0, len(schema))
	for _, s := range schema {
		fn, ok := s["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := fn["name"].(string)
		if !ok {
			continue
		}
		if !want[name] && !mcpAllowedByWildcard(name, allowed) {
			continue
		}
		filtered = append(filtered, s)
	}
	data, _ := json.Marshal(filtered)
	return data
}

// LockRegistry is the one shared advisory-lock registry behind the "lock"/
// "unlock" tools. Exported (unlike the rest of this file's tool wiring) so
// integration tests in package main can assert on real lock state — e.g.
// that a lock taken inside an async subagent job is actually released —
// without a parallel, tools-package-only accessor to keep in sync.
var LockRegistry = locks.NewRegistry()

var toolRegistry = map[string]Tool{
	"bash":   &BashTool{},
	"lua":    &LuaEvalTool{},
	"find":   &FindTool{},
	"todo":   &TodoTool{},
	"read":   &ReadTool{},
	"write":  &WriteTool{},
	"skills": &SkillsTool{},
	"memory": &MemoryTool{},
	"help":   &HelpTool{},
	"agents": &AgentsTool{},
	"cron":   &CronTool{},
	"web":    &WebTool{},
	// Waiter is nil until SetJobWaiter is called; plain wait (no job_id)
	// works without it.
	"wait":   &WaitTool{},
	"lock":   &LockTool{Registry: LockRegistry},
	"unlock": &UnlockTool{Registry: LockRegistry},
	// jobAsker/jobAnswerer/jobProgressReporter/jobResumer are nil until
	// SetJobAsker/SetJobAnswerer/SetJobProgressReporter/SetJobResumer are
	// called; each tool fails loudly (not silently) until then.
	"ask":             &AskTool{},
	"answer":          &AnswerTool{},
	"report_progress": &ReportProgressTool{},
	"resume":          &ResumeTool{},
	// kill_job needs no wiring of its own: it acts on the background-command
	// registry in bgbash.go, which the bash tool populates.
	"kill_job": &KillJobTool{},
}

// SetJobWaiter wires the "wait" tool's job_id polling path (tools/wait.go)
// to a JobWaiter. Called once from main() with an adapter over the app's
// shared jobs.Registry — the same registry /btw side-conversations and the
// "subagent" tool's async spawn path (SetJobStarter, subagent.go) run on —
// so a job started anywhere in the app can be polled via the wait tool.
// This package deliberately does not import "jobs" itself (see JobWaiter's
// doc comment on the import-cycle risk); the caller supplies an
// implementation that satisfies the interface structurally.
func SetJobWaiter(w JobWaiter) {
	if wt, ok := toolRegistry["wait"].(*WaitTool); ok {
		wt.Waiter = w
	}
}

// subagentToolInstance is the singleton SubagentTool used by the registry.
var subagentToolInstance *SubagentTool

// SetSubAgentRunner sets the runner for the subagent tool and registers it.
// Must be called before any subagent tool usage.
func SetSubAgentRunner(runner SubAgentRunner) {
	subagentToolInstance = &SubagentTool{Runner: runner}
	toolRegistry["subagent"] = subagentToolInstance
}

func RunTool(ctx context.Context, name string, arguments map[string]any) ToolResult {
	// Permission first: a tool this caller may not use was never a call, so
	// it should not reach a user hook either. See tools/toolgate.go for why
	// the check lives here and not only in the layer above.
	if err := checkToolGate(ctx, name); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	// User hooks wrap every tool call, built-in and MCP alike, and this is
	// the single place all of them pass through. The pre hook can veto the
	// call; the post hook annotates its result. Both are no-ops (and cost
	// nothing beyond a map lookup) when nothing is configured — see
	// internal/hooks.
	if hooks.Any(hooks.EventPreTool) {
		if blocked, msg := hooks.RunPre(ctx, name, arguments); blocked {
			return ToolResult{Type: "result", Success: false, Error: msg}
		}
	}

	res := runToolInner(ctx, name, arguments)

	if hooks.Any(hooks.EventPostTool) {
		note, fail := hooks.RunPost(ctx, name, arguments, res.Success, res.Content, res.Error)
		if note != "" {
			// The note goes wherever the model is already looking: appended
			// to the content of a success, to the error of a failure.
			if res.Success {
				res.Content = appendNote(res.Content, note)
			} else {
				res.Error = appendNote(res.Error, note)
			}
		}
		if fail && res.Success {
			res.Success = false
			res.Error = appendNote(res.Content, note)
			res.Content = ""
		}
	}
	return res
}

func runToolInner(ctx context.Context, name string, arguments map[string]any) ToolResult {
	if tool, ok := toolRegistry[name]; ok {
		return tool.Run(ctx, arguments)
	}

	// Check MCP tools
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil && mcpRunner.HasTool(name) {
		return mcpRunner.RunTool(ctx, name, arguments)
	}

	return ToolResult{Type: "result", Success: false, Error: "unknown tool: " + name}
}

func appendNote(text, note string) string {
	if text == "" {
		return note
	}
	return text + "\n\n" + note
}

// MaxParallelFor reports the dispatcher's per-tool concurrency limit for the
// given tool name. 0 (the default for tools that don't implement MaxParallel)
// means no limit — calls run concurrently like every other tool. 1 forces
// the dispatcher to run calls of that tool serially inside a single goroutine
// when several appear in the same LLM response. Used by agent.executeTools
// to honour tools whose state is not safe under concurrent calls from the
// same batch. MCP tools are always reported as 0 (no special handling).
func MaxParallelFor(name string) int {
	if mcpRunner := GetMCPToolRunner(); mcpRunner != nil && mcpRunner.HasTool(name) {
		return 0
	}
	tool, ok := toolRegistry[name]
	if !ok {
		return 0
	}
	if mp, ok := tool.(interface{ MaxParallel() int }); ok {
		return mp.MaxParallel()
	}
	return 0
}
