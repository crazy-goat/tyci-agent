# Async tools

This document describes how tool calls run, how a late result reaches the
model, and what the wait tool does. It describes the code as it is in the
current branch.

## Design

1. The agent loop fires each tool call through the dispatcher
   (`agent/dispatcher.go`).
2. The dispatcher waits for the call for a short time.
3. If the call ends in that time, its result goes into the conversation as
   the normal tool result.
4. If the call is still running, the model gets a placeholder result. The
   placeholder reads `started, id=cN; the result arrives later as a tool.result
   notice; wait does not take this id yet`, where `N` is the call number.
5. When the call ends, the result is published on the bus as kind
   `tool.result`. The agent adds it to the conversation as a tail notice.

## Tail notice

A late result has this form:

    <tyci-notice kind=tool.result call_id=cN failed=false duration_ms=1234>
    ...content...
    </tyci-notice>

The notice is a user text block at the end of the conversation. The
conversation before it does not change, so the prompt cache stays valid.

## Large late results

A late result longer than `tools.result_tail_bytes` (default 16384) keeps only its
last bytes in the `tool.result` message. Only the results of background calls are
cut. An inline result is never cut.

The first line of a cut result tells the model how much it sees and where the full
output is:

    [truncated: showing the last 16384 of 1048576 bytes; full output: /home/u/.tyci/tool-results/<session-id>/c1.txt]

The full output is written to `~/.tyci/tool-results/<session-id>/<call_id>.txt`.
The directory is created with mode 0700 and the file with mode 0600. The session id
is unique, so the call ids of two sessions do not collide. The cut moves forward to
the next UTF-8 character start, so the tail can be a few bytes shorter than the limit.

If the file cannot be written, the tail is still sent. The first line then says
`full output could not be saved`, and `full_path` is empty. A chat without an open
session has no directory, so its cut results are never saved.

The `tool.result` payload has the fields `truncated` and `full_path`. The notice
tag shows them as `truncated=true` and `full_path="..."`.

## Wake rule

A turn ends when the model stops calling tools. The loop then waits for the
results of the calls still running, unless a user message is pending. A
pending user message ends the wait, so the person at the keyboard gets the
next turn at once. The wait ends when the last result arrives.

A blocking call (timeout `noDeadline`: `subagent`, `ask_parent`, `wait`, and
every call of a tool that runs one at a time) never moves to the background.
Every call of a subagent blocks, because a subagent has no late-result path:
its dispatcher has no bus and no user poll (`NewChildDispatcher`). Only the
main conversation backgrounds calls.
Wake messages and a pending user message do not change a blocking call. It
returns only when it finishes or when the turn is cancelled. A serial tool
therefore keeps the order of its batch.

The dispatcher never backgrounds `subagent`, `ask_parent` or `wait`. The
`subagent` tool still has its own 60s handoff (`runWithHandoff` in
`tools/subagent.go`). That handoff stays until the cleanup follow-up removes
it.

## Background wait and kill deadline

Two timers apply to a tool call. They are different.

- The background wait is set by `timeoutFor` in `agent/dispatcher.go`. When it
  ends, the call moves to the background. The call keeps running, and the model
  gets the placeholder. Nothing is killed.
- The kill deadline is the timeout of the tool itself, for example the `timeout`
  of `bash`. It is set in `runToolCall` and does not change with this design. When
  it ends, the tool stops.

The background wait has three levels. The first match wins:

1. A blocking tool (`subagent`, `ask_parent`, `wait`) never backgrounds.
2. `tools.background_after_sec.<tool>` in config.json, when the key is set.
3. The class of the tool: `tools.background_after_fast_sec` (default 2.5s) for fast
   tools, and `tools.background_after_slow_sec` (default 60s) for slow tools.

Fast tools are `read`, `find`, `skills`, `agents`, `todo`, `write`, `edit`, `lock`,
`unlock` and `report_progress`. Every other tool is slow. An MCP tool is always slow.
A `0` value backgrounds the call at once. The keys are documented in the README,
section "Tool timeouts before background".

## Wait tool

`wait` takes these parameters:

| Parameter | Meaning |
|---|---|
| `ids` | List of job ids. Repeated ids count once. |
| `mode` | `any` (default) returns when one job finishes or blocks. `all` returns when every job does. |
| `seconds` | Maximum time. Required only for a plain sleep, with no job id. |
| `job_id` | Alias for one id. It gives the same result as `ids` with that one id. |

The wait ends early when a user message is pending or when a new notice
arrives. The jobs are not touched in either case.

`wait` runs one call at a time in a batch (`MaxParallel` is 1).

Not implemented: a `kinds` filter for notices, and a `Seq` based dedupe. The
wait path dedupes by job id only.

## Mutation FIFO

A mutation changes files in a worktree. These calls take a FIFO for their
worktree:

- `write`, which also covers edit mode
- `bash`, unless the call has `parallel=true`
- `ctx.run` and `ctx.write` inside a Lua script

For a batch of tool calls, the dispatcher takes the place of each `write` and
`bash` call in the FIFO before its goroutine starts. It takes the places in
call order, so the mutations of one batch run in call order, even when the
goroutines start in another order. The place is released when the call ends,
on every path. A Lua call takes its place when `ctx.run` or `ctx.write` reaches
the FIFO. A direct caller of a tool takes its place when the tool runs.

Reads do not take the FIFO. Two agents in one worktree share one FIFO. Two
worktrees do not block each other. The FIFO exists only in one tyci process.

## Locks

A `lock` without a `seconds` value lives until the agent ends. The lock tool
takes this lifetime from the agent context, set with `tools.WithAgentCtx`. A
call context that ends does not release the lock.

## Failure paths

- A call that panics returns the text `Error: tool panicked: ...` and is
  marked failed.
- A call cancelled by the user (ESC) returns `cancelled: the turn was
  interrupted`. ESC cancels a call that is still waiting, and it cancels the
  calls that are backgrounded.
- A backgrounded call does not stop when its turn ends. Its real result
  arrives as a late result in a later turn. Only ESC or a close of the
  dispatcher cancels it.
- A call still open when a session is resumed gets a failed result with the
  text `cancelled: session was restarted`.
- A `tyci run` waits for its background calls before it exits.
- Closing the dispatcher cancels the calls that are still running.
- A result that arrives after a run has ended, and no later turn starts,
  is not shown to the model. The result stays in the bus journal.

## Security

- A subagent may wait on and kill only jobs in its own subtree.
- Inside a subagent, an unknown job id is refused.
- Locks are advisory. They do not stop a process from writing a file.
- The FIFO does not stop writes from outside tyci.

## Known gaps

- `subagent` and `ask_parent` block by design. Making `ask_parent` async is
  tracked in issue #200.
- Subagent results do not wake the parent after its run has ended.
- `bash_output` does not exist in this repository. Read output with
  `wait(job_id)`.
- `call_id` is not accepted by `kill_job`. Display events are still matched
  by queue order, not by call id.
- `runWithHandoff`, `watchForWaiting`, the bash `background_after` timer,
  `SetJobObserver` and `AskUnroutableCtxKey` still exist. See `findings.md`.
