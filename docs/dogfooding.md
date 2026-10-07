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
  "roles": {
    "worker": {"model": "strong"},
    "review": {"model": "strong"}
  }
}
```

- `models` maps an alias to a `provider/model` name. Aliases are optional: `default_model` and a role's `model` may also be a direct `provider/model` name (for example, `nexos/GPT 5.6 Luna`).
- `default_model` is used by a role that has no `model`.
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

Version 0.3.0 cannot resume a run after a restart.

1. Quit the TUI with Ctrl+C in its terminal. Or run `kill <pid>` for that process only.
   Find it with `pgrep -fl 'tyci'`.
2. Do not use `pkill -f 'tyci tui'`. It kills other sessions.
3. After a hard kill, `state.json` keeps `status: running`. This is expected.
   A `running` or `paused` run blocks a new `workflow_start` for the same issue.
   Answer a `paused` run with `stop`, or remove its run directory (step 5).
4. Remove the worktree and the branch:

```bash
git -C ~/work/crazy-goat/tyci-agent worktree remove --force ~/.tyci/worktrees/tyci-agent/issue-N
git -C ~/work/crazy-goat/tyci-agent branch -D issue-N
```

5. Remove the run directory. Without this step, the old `running` state blocks a new start for the issue:

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
| `history` | One entry per step: `seq`, `state`, `kind`, `key`, `to`, `exit`, `stderr_tail`, `warnings`, `error` (agent error text, masked) |

New PR comments from team members (write or admin permission) go to `comments.md` in the run directory; the next `code` visit reads it. `fetch_comments.sh` also writes `last_comment_id` and `last_review_comment_id` there, and the runner copies them to `state.json`.

The reviewer writes `review.md` in the run directory (`$TYCI_RUN_DIR`). Its first line
is `ACCEPT` or `CHANGES`.

## 11. Override the defaults

| What | Where |
|---|---|
| Workflow | `.tyci/workflows/<name>.json`, then `~/.tyci/workflows/<name>.json`, then the built-in `issue-to-merge` |
| Check scripts | `.tyci/checks/`, then `~/.tyci/checks/` |
| Config | `.tyci/config.json` over `~/.tyci/config.json` |

`tyci` reads project files (`.tyci/...`) only for trusted projects.
