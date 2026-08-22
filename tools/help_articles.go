package tools

// Long-form tool documentation, returned by the "help" tool on request.
//
// Kept out of the schema deliberately. A schema description is re-sent with
// every request, so the cost of a worked example there is paid on every turn
// forever; here it is paid only when the model asks for it. That trade is what
// makes it worth writing these properly.
//
// Only tools where the long version changes behaviour have an entry. A tool
// whose one-line description already says everything falls back to its schema
// (see HelpTool.Run), so this map is a list of what repays explanation, not a
// list of everything.
var toolHelp = map[string]string{
	"lua": `Run a Lua script that calls other tools. This is the highest-leverage
tool available, and the reason is not speed.

Two costs disappear at once:

  Time — every tool call is a round trip. A script pays one for the whole loop.
  Context — everything YOU read stays in the conversation for the rest of the
  session and is re-sent with every later request. Everything a SCRIPT reads is
  discarded when it returns. Forty files read by a script cost you one sentence
  of context; forty files read by you cost forty files, permanently.

Available inside a script:

  tool(name, args)  -> {success, content, error}. Any tyci tool, MCP included.
  log(...)          -> progress; streamed live and included in the result.
  json_encode/json_decode
  args              -> the table passed in via the "args" parameter.
  return value      -> handed back; a table comes back as JSON.

Sandboxed to the pure part of the language: string, table, math, coroutine and
the os clock functions. io, os.execute, require, load and loadfile are not
available — tool() is the only way out, which is what keeps hooks, the write
freshness guard and a subagent's tool allowlist in force.

Find, act on each result, return the conclusion. Nearly everything is this:

  local hits = tool("find", {method="grep", pattern="oldName",
                             include={"**/*.go"}, output="files"})
  if not hits.success then return "grep failed: " .. hits.error end
  local changed, failed = 0, {}
  for path in hits.content:gmatch("[%w%./_-]+%.go") do
    tool("read", {path = path})            -- the write guard requires a read first
    local r = tool("write", {path = path, oldString = "oldName",
                             newString = "newName", occurrence = "all"})
    if r.success then changed = changed + 1
    else table.insert(failed, path .. ": " .. r.error) end
  end
  log("changed " .. changed .. " files")
  return {changed = changed, failed = failed}

Read many, return little — an audit that costs one sentence of context:

  local files = tool("find", {method="glob", pattern={"tools/*.go"}})
  local missing = {}
  for path in files.content:gmatch("tools/[%w_]+%.go") do
    local body = tool("read", {path = path}).content
    if body:find("func %(t %*%w+Tool%) Run") and not body:find("Name%(%) string") then
      table.insert(missing, path)
    end
  end
  return missing

Fan out over something you had to discover first:

  local tasks = {}
  for pkg in tool("bash", {command="ls -d */", description="packages"}).content:gmatch("[%w_]+") do
    table.insert(tasks, {task = "Review package " .. pkg ..
                                ". Return file:line for every TODO and FIXME."})
  end
  return tool("subagent", {tasks = tasks, async = true}).content

Rules that matter:

  - Check res.success on every call. A script that ignores a failure returns a
    confident wrong answer, and nothing else will catch it.
  - log() as you go. A script that dies half way still tells you where it got.
  - Return the conclusion, not the material. That is the entire point.
  - Make loops terminate. Aborted after its timeout (default 300s) and after
    500 tool calls.

Do NOT use it for one or two calls — that is pure overhead — or for work that
needs judgement about content. A script applies a rule you already know; it
cannot decide for you.`,

	"subagent": `Delegate work to a child agent that has its own context window.

The reason is the same as for lua, one level up: a child reads into its own
window and hands you back only its conclusion. Where lua applies a rule you
already know, a subagent can decide things — at the cost of a startup and a
summarisation round trip, so it is not for small work.

Parallel work, stated as rules:

  - Two or more independent tasks go out in ONE call with tasks=[...]. They run
    in parallel, so two tasks cost about as much wall-clock as the slower one.
    Two separate calls are strictly worse.
  - async=true whenever you do not need the result this turn. You get job_ids
    back at once and a notice when each finishes; read one with wait(job_id=...).
  - If you are told a job is waiting for an answer, answering it is your NEXT
    action. answer(job_id=..., text="...") is the only channel it has, it makes
    no progress meanwhile, and its work is discarded when it times out.
  - Tasks touching the same files must be told to lock/unlock the paths they
    write. The write freshness guard will otherwise fail one of them, loudly.

Writing the task is the whole job. The child gets your task string and nothing
else — no history, no earlier findings, no assumptions. State what to do, which
paths to look at, and what to return, in that order:

  bad:  "Investigate the login flow"
  good: "Read internal/auth/*.go. List every function that writes to the
         session cookie. Return file:line and a one-line description each."

agent="name" runs it under a named definition from ./.tyci/agents/ or
~/.tyci/agents/, which supplies its system prompt and may pin its model,
max_iterations, max_tokens and allowed tools. Call the "agents" tool for the
current list — the one in your system prompt goes stale if a definition is
added mid-session.

Bound anything that might wander with max_iterations. A child that hits the cap
returns what it had, marked truncated.`,

	"write": `Write a file, or replace exact text in one.

Two modes, and mixing them up is the usual mistake:

  content + range?          -> write mode. Replaces the whole file unless range
                               narrows it. range: a line number, "from...to",
                               "before:N", "after:N", "all", or "append".
  oldString + newString     -> edit mode. Replaces exact text. Triggered by the
                               presence of oldString.

Edit mode requires oldString to match exactly once, unless occurrence says
otherwise (a number, or "all"). Include enough surrounding text to be unique —
that requirement is what stops an edit landing in the wrong place. dryRun=true
previews without writing.

Freshness. A file that already exists may only be modified if you read it first
with the "read" tool and it has not changed since. Both modes replace whole
files, so writing from a stale copy silently discards work you never saw: a
human saving in their editor, a generated file, a parallel subagent's change.
When a write is refused for this reason, read the file again and redo the edit
against what it says NOW. Do not retry the same call — it will fail the same
way, and if it did not, it would destroy something.

Creating a new file and range="append" need no prior read.`,

	"bash": `Run a shell command. Use it when no other tool fits — "find" is better at
searching, "read" and "write" are better at files, and both are cheaper.

Backgrounding is the part worth knowing. A command still running after 30s is
moved to the background: you get a job_id, the command keeps running, and you
get a notice when it finishes. Read its output with wait(job_id=...) or stop it
with kill_job(job_id=...).

  Never re-run a command that was moved to the background. A second copy races
  the first, and for a build or a migration that is how you get corrupt output.

  run_in_background=true starts something you already know is long (a build, a
  test suite, a watcher) and returns immediately, so you can work meanwhile.

  timeout raises the total wall-clock limit (default 120s). It is a limit, not
  a promise to block — the handoff at 30s still happens.

  background_after=0 is the explicit opt-out: stay in the foreground until the
  command finishes or hits its timeout.

Backgrounding is only available to the top-level agent in an interactive
session. A subagent's run ends when it returns, so it must block on its long
commands instead.`,

	"find": `Two searches in one tool, chosen by method.

  method="glob"  — find files by path pattern. pattern takes one glob or a list.
                   "**" crosses directories, "*" does not.
  method="grep"  — search inside files. mode="text" (default), "word" or
                   "regex"; include/exclude narrow which files are searched;
                   context=N returns N lines around each hit; output="lines"
                   (default), "files" or "count".

Both respect .gitignore, skip binary files, and cap their results — the reply
says when a cap was hit, so a truncated answer is never mistaken for a complete
one.

Reach for output="files" or output="count" when you are deciding where to look,
and "lines" only when you need to read the hits. Over many matches that is the
difference between a list of paths and a wall of code in your context.

This tool is why you rarely need "bash grep": the output is bounded and the
ignore rules are already applied.`,

	"memory": `Notes about this project that survive the session.

Every note is loaded into your system prompt at the start of a session, so
writing one is how something you worked out stops having to be worked out
again. That also means every note is re-sent with every request for the rest of
the project's life, which is why the bar for writing one is high.

  memory(action="write", name="test-command",
         content="make check runs the golden tests; go test ./... skips them.")
  memory(action="list")      -- also "read" and "delete"

Write a note for something NOT obvious from reading the code:

  - the command that actually builds or tests this project
  - a rule the compiler does not enforce (a package that must not import
    another, a naming convention that matters)
  - a decision and the reason behind it
  - a trap you already fell into

Do not write notes about what the code plainly says, about the details of one
task, or about the current conversation. Those are noise you will pay for on
every future request.

Say WHY, not just what, and keep it to a few sentences. Correct a note that
turns out to be wrong by writing it again under the same name; delete one that
no longer applies. A note written now cannot appear in this session's prompt —
it is already on disk, and it will be there next time.`,

	// "jobs" is not a tool. It is here because the orchestration story is the
	// one thing this environment does that a model has no prior for, and it
	// was previously told in fragments: each of ask, answer, wait, resume and
	// lock described its own step and nothing described the lifecycle. See
	// HelpTool.Run for why a non-tool key is allowed.
	"jobs": `Not a tool — the lifecycle that subagent, wait, answer, resume, lock and
kill_job are parts of. Read this once.

Spawn.

  subagent(tasks = [{task = "..."}, {task = "..."}], async = true)

One call, both children run in parallel, and you get their job_ids back
immediately. Two separate calls are strictly worse: same cost, half the
concurrency.

A call without async=true blocks, but not forever: after 60 seconds the
children move to the background exactly as if you had asked for async, and the
turn ends. That matters for the person watching — they get their prompt back
and can keep talking to you instead of waiting on a spinner.

You are notified. You do NOT poll. When a child finishes, or blocks on a
question, a notice reaches you at the start of your next turn — and wakes an
idle session if there is no next turn yet. Anything that tells you to poll is
out of date.

  notice: finished  -> wait(job_id = "...") reads the result.

wait(job_id) is a wait for the RESULT, not a sleep: it stays there until the job
finishes or blocks on a question, and ends early if someone types. So one call
gets you the answer. It is still not a substitute for the notice — calling it
before you are told just means standing in a queue you were never in.
  notice: waiting   -> answer(job_id = "...", text = "...") NOW.

The second one is urgent and the reason this page exists. A blocked child makes
zero progress while it waits, and when its wall clock runs out everything it
did is discarded. One answer() call is the difference between a finished task
and nothing at all. If you try to end your turn with one outstanding, the
harness will remind you — but by then you have already wasted a round trip.

While they run, you work. That is the whole point of async: read something,
edit something, spawn more children. What you must not do is sit in
wait(seconds = 30) hoping — that is polling with extra steps.

Follow up cheaply.

  resume(job_id = "...", task = "now also update the tests")

The finished job keeps its entire conversation, so a follow-up costs no
re-explaining. Spawning a fresh child and describing the context again is the
expensive way to do the same thing.

Shared files.

  lock(path = "internal/auth")   -- returns a holder id
  unlock(path = "internal/auth", holder = "...")

Advisory: it tells other agents to keep off, it does not stop them. Two
children editing the same file without this will have one of their writes
refused by the freshness guard — loudly, which is better than silently, but it
still wastes a child. Put the locking instruction in the task text: the child
does the locking, not you.

Stopping things. kill_job(job_id) kills a backgrounded SHELL command and
everything it spawned; its output so far stays readable with wait. It does not
stop an async subagent.

From inside a job. A child can report_progress(text) so watchers are not
guessing, and ask(question) to block on the parent — that last one is a last
resort, because asking is how a child stalls. Every subagent call gets a job
id, blocking or async. ask still needs a way for an answer to reach it,
though: if the call that spawned this job has no way to hand off and free
itself up, ask fails immediately instead of stalling for nothing.`,

	"cron": `A prompt that runs later, or over and over, with nobody there.

  cron(action="add", name="nightly-tests", schedule="at 02:00",
       prompt="run the test suite and summarise the failures")
  cron(action="list")                 what is scheduled, and when each runs next
  cron(action="logs", name="...")     what the last runs actually did
  cron(action="run_now", name="...")  run it out of turn, to check it works
  cron(action="disable"|"enable"|"remove", name="...")

Schedules. "every 30m", "every 6h" — measured from the END of the last run, so
a job cannot overlap itself; the shortest is a minute. Or "at 07:30" — local
time, once a day. Nothing else parses, on purpose: a crontab expression that is
subtly wrong simply never fires, and nobody notices.

The prompt is the whole job. A run is a FRESH agent with only that text: no
history, no findings from this conversation, no idea what you were doing. Write
it the way you would write a task for a subagent — what to do, which paths, what
to report. "Check the thing we found earlier" schedules nothing useful.

The directory is recorded when you add it, not resolved when it runs, so the job
keeps meaning the same project.

Who runs it. An interactive session ticks the schedule; a one-shot run does not.
A new job is due at once, so if a session is open you find out immediately
whether it works. If none is, the job simply waits — say that plainly instead of
telling someone their job is running.

You are notified when a scheduled run finishes, including runs nobody asked for.
Never poll for one.

What it is for: work that repeats or belongs to a later time — a nightly test
run, a periodic check on a queue, a summary before someone starts their day. It
is not a reminder to yourself inside this conversation, and it is not somewhere
to park work you were asked to do now.`,

	"todo": `The run's task list, and a gate: non-todo tool calls are refused until at
least one item exists. Plan first, then act.

  todo(action="add", content="...")            one item
  todo(action="add_batch", items=[...])        several at once
  todo(action="doing"|"done"|"blocked", id=N)  move an item
  todo(action="list")

Keep items at the granularity of something that can be finished and verified —
one per file you intend to change, not one per keystroke and not one for the
whole request. Mark an item done when it is actually done, and blocked (with a
reason) when it cannot proceed; the turn will not end quietly with items still
open, so a stale list nags you rather than the user.`,
}
