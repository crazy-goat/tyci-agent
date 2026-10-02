# Test plan: subagents, background jobs, lock, /btw

Manual test plan for the functionality added/changed in this round of work:
`jobs/`, `eventbus/`, `tools/wait.go`, `locks/` + `tools/lock.go`, async
subagent spawn (`subagent(async: true)`), the background jobs panel in the TUI
(`Ctrl+B`) and `/btw`. Each scenario: steps, expected result, where to look
for the problem if the result does not match.

Run `tyci` in TUI mode (not `--print`/console) for scenarios involving
panels/modals — those modes have no UI to observe.

## 0. Baseline — nothing should break

Goal: make sure the old, synchronous `subagent` behaves identically to how it
did before this whole round of changes.

- [ ] **0.1** `subagent(task: "policz do 5 i zwróć wynik")` (without `async`) — <!-- english-ok -->
      blocks the turn, the subagent modal shows a live stream, the result comes
      back as text in the same turn.
- [ ] **0.2** `subagent(tasks: [{task:"A"},{task:"B"}])` (two parallel, sync)
      — both run in parallel, the modal shows a mixed stream from both
      (a known, accepted flaw — see `docs/architecture-refactor.md` /
      earlier decisions), the result is a JSON array of two results.
- [ ] **0.3** `subagent(agent: "reviewer", task: "...")` — the named agent from
      `internal/agentdefs/builtin/reviewer.md` works: model/tools/temperature
      from the frontmatter are respected.
- [ ] **0.4** `subagent(agent: "nieistniejący", task: "x")` — hard error <!-- english-ok -->
      ("agent not found"), not a silent fallback to a plain subagent.

## 1. Async spawn (`subagent(async: true)`)

- [ ] **1.1** `subagent(task: "coś co potrwa ~30s", async: true)` — the turn <!-- english-ok -->
      **immediately** gets a result with `job_id`, it does not wait for completion.
- [ ] **1.2** Right after 1.1: the **background jobs panel** (bottom bar, above the
      input field) shows a new entry with status `running`.
- [ ] **1.3** After the job finishes: the panel updates the status to `done` (or
      `failed`/`truncated`) live, without refreshing/interaction.
- [ ] **1.4** `Ctrl+B` opens a modal with a list of all background jobs (newest
      on top); `Enter` on an entry shows the `Result`/`Err` of the job.
      If the job is still `running` — the modal shows "still running", not an
      empty result.
- [ ] **1.5** **Regression we fixed**: while the job from 1.1 is still
      running, check that **nothing leaks** into the closed tool block in
      the main transcript view (the "subagent" block should show up as
      finished immediately after returning `job_id`, and should not later
      "append" live text from the background job).
- [ ] **1.6** `subagent(tasks: [{task:"A", async:true}, {task:"B", async:false}])`
      (mixed) — hard error "cannot mix async and non-async", zero jobs
      started.
- [ ] **1.7** `subagent(tasks: [{task:"A", async:true}, {task:"B", async:true}])`
      — both start immediately, two separate `job_id`s in the result, the panel shows
      two entries.
- [ ] **1.8** Panel/modal empty (zero-height, takes no space) when nobody
      has ever used `async: true` in the current session.

## 2. `wait`

- [ ] **2.1** `wait(seconds: 5)` (without `job_id`) — the model gets control
      back after ~5s with the message "waited 5s; check status now.".
- [ ] **2.2** `wait(seconds: 5, note: "waiting for the build")` — `note` appears
      in the returned message.
- [ ] **2.3a** `wait(seconds: 0)` — clamped to `MinWaitSeconds` (1), returns after
      ~1s with a clamping annotation.
- [ ] **2.3b** `wait(seconds: 99999)`, **but interrupt with ESC after ~5s** instead of
      waiting for the end — do not wait the full clamped 1800s (`MaxWaitSeconds`)
      in the manual test. Only check that the result contains the annotation about
      clamping to the maximum (the clamp itself is already covered by the unit test
      `TestWaitTool_ClampsHigh`, no need to sit through it manually).
- [ ] **2.4** Spawn a job via 1.1, then `wait(job_id: "<id from 1.1>", seconds: 60)`
      — if the job finishes before 60s, `wait` returns **immediately** with its
      result (it does not wait the full 60s).
- [ ] **2.5** `wait(job_id: "<id of a job that is still running>", seconds: 3)` —
      returns after 3s with "still running after 3s (job_id=...). Call wait again...",
      **Success: true** (this is not an error).
- [ ] **2.6** `wait(job_id: "nieistniejące-id", seconds: 5)` — error "unknown <!-- english-ok -->
      job_id".
- [ ] **2.7** ESC/cancellation during `wait(seconds: 60)` — interrupts
      immediately (does not wait until the end), returns "wait cancelled after ~Ns".

## 3. `lock` / `unlock`

- [ ] **3.1** `lock(path: "foo/bar.go")` — success, `Content` contains `holder`
      (e.g. `holder-abc123`) — note it down for the steps below.
- [ ] **3.2** A second `lock(path: "foo/bar.go")` (different holder, same path)
      **while the first lock is still held** — error with information about who
      holds it and since when/until when.
- [ ] **3.3** `unlock(path: "foo/bar.go", holder: "<holder from 3.1>")` — success,
      path unlocked; a subsequent `lock` on the same path now succeeds.
- [ ] **3.4** `unlock(path: "foo/bar.go", holder: "wrong-holder")` — error (holder
      does not match), the lock remains active.
- [ ] **3.5** `lock(path: "x", seconds: 3)` — after ~3s the path is automatically
      unlocked (a subsequent `lock(path:"x")` without waiting for `unlock` succeeds).
- [ ] **3.6** `lock(path: "x")` **without** `seconds` — the lock is held until the end of
      the session/context (does not expire on its own in a short time).

## 4. `/btw`

- [ ] **4.1** During a normal conversation: `/btw jakie mamy dziś testy jednostkowe w tools?` <!-- english-ok -->
      — the modal opens **immediately**, shows a live stream of the answer.
- [ ] **4.2** While 4.1 is running (btw modal open or closed), keep typing
      in the main thread — **the main thread is not blocked**, you can
      continue the conversation normally.
- [ ] **4.3** After the btw from 4.1 finishes: check that the btw answer **never
      appears** in the main history/transcript — it is purely a side branch.
- [ ] **4.4** Close the btw modal (ESC) while it is still streaming — the job
      keeps running in the background (check via the panel from section 1 or by reopening
      from the list), it is not killed by merely closing the window.
- [ ] **4.5** Bare `/btw` (without a question) — opens a **list** of previous btws from
      the current session (question, status, truncated fragment of the answer).
- [ ] **4.6** From the list in 4.5: selecting an old entry (Enter) shows its full
      content in preview mode (static, not live if already done).
- [ ] **4.7** `/btw` asked right after some contextual fact came up in the
      conversation (e.g. a file name) — check that btw **sees** this
      context (answers accurately, without asking "what are you referring to") — the fork copies
      `msgs` at the moment of the call.
- [ ] **4.8** Two `/btw` fired one after another (the second before the first
      finishes) — both work independently, they do not overwrite each other's modal/stream
      state.

## 5. Cross-cutting / edge cases

- [ ] **5.1** Restart `tyci` (new process) — jobs panel empty, btw list
      empty (everything is in-memory, nothing survived the restart — this is expected,
      not a bug).
- [ ] **5.2** Closing `tyci` (Ctrl+C) **while** the async job from 1.1 or the
      btw from 4.1 is still running — the process should exit without hanging
      (the goroutine is detached from the session context, but should not block
      shutdown).
- [ ] **5.3** `--print`/console mode (non-TUI): `subagent(async: true)`
      still returns `job_id` correctly despite there being no panel to show it
      (the panel exists only in the TUI — check that this does not crash in the mode without
      TUI).
- [ ] **5.4** `go test -race ./...` in the repo — green as a baseline before
      the manual tests from this document.

## What is NOT implemented yet (do not test, absence expected)

- Auto-`/compact` at 95% of context — planned, not implemented.

## 6. `ask_parent`/`answer_job`, `report_progress`, `resume`

- [ ] **6.1** While an async job is running (spawned via 1.1): the job calls
      `ask_parent(question: "...")` — the background jobs panel shows status
      `waiting_answer` (instead of `running`), and `wait(job_id: "<id>")` returns
      a message containing the exact text of the question and an instruction to use
      the `answer_job` tool with this `job_id`.
- [ ] **6.2** Right after 6.1: call `answer_job(job_id: "<id from 6.1>", text: "...")`
      from the main thread (or from another agent) — the job unblocks
      immediately, `ask_parent` inside the job gets back exactly this text,
      the status returns to `running`, and the final result of the job (visible via
      `wait`) reflects the received answer.
- [ ] **6.3** `ask_parent` that **nobody answers** — the job **does not hang
      forever**: it unblocks by itself upon reaching the job's own
      timeout (1800s for an async subagent), returning a message saying
      explicitly that there was no answer and that the agent should continue on its own.
      (In the manual test do not wait the full 1800s — the unit/
      integration tests already cover this with a shorter, injected deadline;
      manually it is enough to confirm that `ask_parent` is available only
      inside a background job, not from a normal, foreground turn.)
- [ ] **6.4** An async job calls `report_progress(text: "...")` while
      running (before finishing) — `wait(job_id: "<id>")` called while the
      job is still running contains in the "still running" message the exact text of the
      last reported progress; the same text is also visible after the
      job finishes (progress is not cleared after `done`).
- [ ] **6.5** `resume(job_id: "<id of a finished async job>", task: "...")` —
      you get a **new, different** `job_id`; poll it via `wait` like any other
      async job. The new task should refer to something that came up
      **only** in the first turn (e.g. ask the model to repeat a number/
      file name mentioned solely in the original task) — check that the
      resumed job actually **sees** that earlier context, rather than
      starting from scratch.
- [ ] **6.6** `resume` on a `job_id` that does not exist or was never
      resumable (e.g. a synchronous `subagent` without `async:true`, or a job
      that ended with a hard error) — a clean error with a sensible description,
      with no crash and no process hang.
