# Dogfooding: run one issue from start to merge

`tyci` can work on a GitHub issue of its own repository: it writes the code, has it
reviewed, pushes, waits for CI and merges. This page shows one human how to run
that loop once. Every command can be copied as it is.

## Safety model

- A gate script (`internal/flow/checks/issue_done.sh`) starts work only if the issue is
  open, has the label `accepted` and its author has write or admin access.
- The merge waits for the required check `ci-ok`. Push uses explicit refs.
- A pull request that touches `.github/**`, `.tyci/**` or `internal/flow/checks/**`
  stops at `ask` before the merge. A human merges it.
- One run works at a time.

## 1. Install the binary

Install from a clean `main`. Never install from a worktree you are editing.

```bash
cd ~/work/crazy-goat/tyci-agent && git switch main && git pull
make install            # installs to ~/local/bin
cp ~/local/bin/tyci ~/local/bin/tyci.prev   # optional safety copy before the next install
which tyci
tyci --version
```

## 2. Configure once

Create `~/.tyci/config.json`. Use `$ENV_VAR` references for secrets, never real keys.
The file accepts only the keys shown here and `check_timeout_sec`.

```json
{
  "models": {
    "fast": "my-provider/my-fast-model",
    "strong": "my-provider/my-strong-model"
  },
  "default_model": "fast",
  "default_effort": "medium",
  "roles": {
    "worker": {"model": "strong", "effort": "high"},
    "review": {"model": "strong"}
  }
}
```

- `models` maps an alias to a `provider/model` name. Aliases are optional: `default_model` and a role's `model` may also be a direct `provider/model` name (for example, `nexos/GPT 5.6 Luna`).
- `default_model` is used by a role that has no `model`.
- `effort` on a role sets the reasoning effort (`low`, `medium`, `high`, `xhigh` or `max`). `default_effort` is used by a role that has no `effort`. A `?reasoning=` option in the model URI wins over both. If none is set, the provider default applies.
- `roles` is optional. The roles are `worker`, `review` and `merge_decision`.
  A role may set `prompt`; `"@file.md"` reads a file next to the config file.

Add the provider first, see "Quick Start" in the [README](../README.md).

## 3. Prepare an issue

Pick a small issue. Check the author, then add the label.

```bash
gh issue view N -R crazy-goat/tyci-agent --json author,labels
gh issue edit N -R crazy-goat/tyci-agent --add-label accepted
```

## 4. Start

Go into a clone of the repository, start the TUI and ask for the issue.

```bash
cd ~/work/crazy-goat/tyci-agent
tyci tui
```

Type `work on #N`. The assistant calls the tool `workflow_start` and returns a run id.
Ask "status of the run" to call `workflow_status`.

## 5. Watch

```bash
jq '.status, .current, (.history[] | [.seq,.state,.key,.to])' ~/.tyci/runs/tyci-agent/*/state.json
ls ~/.tyci/worktrees/tyci-agent/
```

The code is in `~/.tyci/worktrees/tyci-agent/issue-N` on branch `issue-N`.

## 6. Answer a paused run

When the run reaches the state `ask`, `status` is `paused`. Tell the assistant to
resume with `retry` (go back to `code`) or `stop` (end the run). The assistant calls
`workflow_resume`. The pause notice names the allowed answers.

Two more answers work at every `ask`. `retry <note>` goes back to `code` and puts the
note into the next worker prompt. `goto <state>` continues the run at that state, for
example `goto rebase`; an unknown state, an ask state or an end state is rejected.

Each role agent of a run is a job named `<run-id>/<role>`, for example
`20261007-102102-527/worker`. Use the `message` tool on a live agent. If a role runs
twice, the name points to the newest job. The `resume` tool does not accept this name.

If an earlier run of the same issue stopped and left its pull request open (branch
`issue-N`), the new run continues that pull request. The state `open_pr` moves the
worktree to the head of the pull request and the run goes to `lock`, `update` and `ci`.
It does not code the issue again. Red CI or a merge conflict goes to `code` as usual. A
conflict only in `CHANGELOG.md` is resolved without an agent (both entries stay). If the
worktree has its own commits that are not on the pull request, or a push finds that the
branch diverged, the run pauses at `ask` and the pause message says what to decide.

The loop limits its retries with `max_visits`: `code` runs at most 3 times and `ci`
at most 3 times. At the limit the run pauses at `ask`.

## 7. Negative checks

Run these once to see that the safety model works.

| Case | Expected result |
|---|---|
| Issue without the label `accepted` | `check_done` goes to `end`. No worktree is created. |
| Issue by an author without write access | The run is skipped. |
| Red CI three times | The run pauses at `ask`. No merge happens. |

## 8. Protected paths

If the pull request touches `.github/**`, `.tyci/**` or `internal/flow/checks/**`,
the run stops at `ask` before the merge. Review and merge the pull request yourself.

## 9. Stop and clean up a stuck run

A `running` run survives a crash or a kill (`kill <pid>`, `kill -9`). The next `tyci` or
`tyci console` start in the same repository resumes it at its saved state. A normal quit
does not leave a run to resume.

1. Quit the TUI with Ctrl+C in its terminal. A normal quit cancels the active runs and saves
   them as `failed` (reason `cancelled`). The next start does not resume them.
2. If the TUI does not respond, run `kill <pid>` for that process only. Find it with
   `pgrep -fl 'tyci'`. Do not use `pkill -f 'tyci tui'`. It kills other sessions.
3. After a kill or a crash, `state.json` keeps `status: running`. The next start resumes the
   run. To stop it for good, remove its run directory (step 5) before you start `tyci` again.
   A run resumed 3 times pauses for an answer.
   A `running` or `paused` run blocks a new `workflow_start` for the same issue.
   Answer a `paused` run with `stop`, or remove its run directory (step 5).
4. Remove the worktree and the branch:

```bash
git -C ~/work/crazy-goat/tyci-agent worktree remove --force ~/.tyci/worktrees/tyci-agent/issue-N
git -C ~/work/crazy-goat/tyci-agent branch -D issue-N
```

5. Remove the run directory. Without this step, the next start resumes an old `running` run, and a `running` or `paused` run blocks a new start for the issue:

```bash
rm -rf ~/.tyci/runs/tyci-agent/<run>
```

## 10. Read `state.json`

The file is `~/.tyci/runs/<repo>/<run>/state.json`.

| Field | Meaning |
|---|---|
| `status` | `running`, `paused`, `done` or `failed` |
| `reason` | Why the run paused or failed |
| `current` | The current state name |
| `ask` | The question and allowed answers, when paused |
| `pr` | The pull request number, when known |
| `last_comment_id` | Highest PR issue comment id handled (`last_review_comment_id`: the same for PR review comments; the two id sequences differ) |
| `visits` | Visit count per state |
| `pid` | The owner process of a `running` run. A dead owner makes the run resumable at the next start |
| `resumed` | How many times the run was resumed after a restart (at most 3, then it pauses) |
| `agent_seq` | The run-wide agent counter |
| `history` | One entry per step: `seq`, `state`, `kind`, `key`, `to`, `exit`, `stderr_tail`, `warnings`, `error` (agent error text, masked), `artifact` (artifact dir name) |

Every check and agent step gets its own artifact dir, in execution order:

```
~/.tyci/runs/<repo>/<run>/artifacts/
  001-check_done/output.log
  002-code/report.md
  003-review/report.md
  004-ci/output.log
  004-ci/ci-failed.log
```

- `NNN` is the step `seq`; the history entry names the dir in `artifact`. An `ask` answer has a
  `seq` but no dir. Resume continues the numbering.
- The runner creates the dir before the step and does not write to it after the step. A leftover
  dir of an aborted step with the same `seq` is removed when the step starts.
- A check gets the path in `TYCI_ARTIFACT_DIR`. The runner writes the script stdout and stderr to
  `output.log` (`stderr_tail` in `state.json` keeps only the last 2 KiB).
- `ci_wait.sh` writes the failed job log of the CI run (`gh run view <id> --log-failed`) to
  `ci-failed.log` when it returns `red`. A `gh` failure there does not change the key.
- After the step, every file in the dir has its secrets masked and is cut to 64 KiB: the tail stays,
  after a truncation line. Files are mode 0600, dirs 0700.

Every agent (worker, review, merge_decision, findings) must write `report.md` in its artifact dir
before it ends: what it did, the result, what is left.

- The task text of every agent has a "Run so far" section: the steps since the last visit of
  its state (all steps on the first visit), each with `seq`, state, key and the absolute paths
  of its artifact files, plus the agent's own artifact dir. The agent reads the files with the
  `read` tool.
- After the agent ends, the runner checks that `report.md` exists and is not empty. If not, it
  sends one message in the same session: "You did not leave your artifact at <path>. Write it
  now." After 2 reminders without a report, the run pauses in `ask` with the reason
  `no artifact from <role>`.
- The reviewer's `report.md` is the review. Its first line is `ACCEPT` or `CHANGES`.
  `post_review.sh` posts the newest `NNN-review/report.md` (highest `NNN`).

New PR comments from team members (write or admin permission) go to `comments.md` in the artifact
dir of the `comments` step; the next `code` visit sees the file in "Run so far".
`fetch_comments.sh` also writes `last_comment_id` and `last_review_comment_id` in the run
directory, and the runner copies them to `state.json`.

## 11. Override the defaults

| What | Where |
|---|---|
| Workflow | `.tyci/workflows/<name>.json`, then `~/.tyci/workflows/<name>.json`, then the built-in `issue-to-merge` |
| Check scripts | `.tyci/checks/`, then `~/.tyci/checks/` |
| Config | `.tyci/config.json` over `~/.tyci/config.json` |

`tyci` reads project files (`.tyci/...`) only for trusted projects.
