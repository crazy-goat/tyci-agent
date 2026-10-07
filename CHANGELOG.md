# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Flow: every agent visit of a run writes a redacted transcript `~/.tyci/runs/<repo>/<run>/agents/NNN-<role>.jsonl` (`NNN` is `agent_seq`) plus a `.md` dump; these agents write no file to `~/.tyci/sessions/<project>/agents/`. Finished runs older than `logs.retention_days` (default 30, `0` keeps all) are deleted at start and every 24 h (#183).
- Models: `?reasoning=<effort>` now also sets `reasoning_effort` (Chat Completions) and the Gemini `thinkingConfig` budget; the catalog keeps `reasoning`, `reasoning_options` and `tool_call` (#120).
- Flow: every agent must write `report.md` in its artifact dir; the runner reminds it twice in the same session, then pauses the run with the reason `no artifact from <role>`. Every agent task lists the run so far (steps since its last visit with their artifact files). The review verdict now comes from the review `report.md`, which `post_review.sh` posts, and `fetch_comments.sh` writes `comments.md` into its artifact dir; the run dir has no `review.md` or `comments.md` any more (#340).
- Flow: every agent step in `state.json` history gets `stats` (model, input, output, cache read/write tokens, `cost_usd`, `turns`, `tool_calls`). `workflow_status` returns it per step plus `totals` and per-role `roles`; the Runs tab shows one line per step and a total line per run (#314).
- Orchestrator: the TUI shows one notice per start-up stage (reading the forge, issue count, planning, fallback reason) before the greeting (#312).
- Watchdog: a running subagent with no activity for `watchdog.idle_after` (default 3m) gets a notice sent to its parent; after each further `watchdog.escalate_after` (default 3m) the notice goes one level up, ending at the human, once. It never kills or nudges a job. Global `config.json` only; invalid durations fail at start (#185).
- Flow: `tyci` resumes runs from `state.json` after a crash or kill, at the saved state and without a new visit; `state.json` gets `pid`, `resumed` and `agent_seq`, and `merge.sh` returns `merged` for a PR that is already merged (#182).
- Cron: new one-shot schedule `in 5m` (stored as `once <RFC3339>` in `~/.tyci/cron.json`); the job is removed after its run and the log stays. New `caller` field in a job records who scheduled it (#316).
- Flow: every check and agent step of a run gets an artifact dir `~/.tyci/runs/<repo>/<run>/artifacts/NNN-<state>/` (new `state.json` history field `artifact`, new check env `TYCI_ARTIFACT_DIR`); checks save their full stdout and stderr to `output.log`, and `ci_wait.sh` saves the failed CI job log on `red` (#339).
- Config keys `compact_soft_limit` and `compact_hard_limit` (config.json, agents.json, agent frontmatter, flow roles) set the context notice and auto-compaction limits in tokens. Subagents and flow roles now compact automatically past the hard limit (#304).
- Flow: role agents of a run are jobs named `<run-id>/<role>` (the `message` tool reaches them), and an `ask` answer can be `retry <note>` (the note goes into the next worker prompt) or `goto <state>` (#327).
- Flow: the final review is posted as a PR review, and new PR comments from team members go back to the worker (#187).
- TUI: new sidebar tab "Runs" shows recent flow runs with status, current state, role, time in state and last steps (#296).
- Orchestrator: `workers > 1` now runs several issues at the same time, and the roadmap oracle order replaces `fallbackOrder` (#283).

### Changed
- Subagents that run as jobs write their own session file to `~/.tyci/sessions/<project>/agents/<time>_<id>_<job-id>.jsonl` plus a `.md` dump. `tyci session list` does not show them, and nothing deletes them yet (#121).
- Context notice now starts at 80% of the window (was 50%) and auto-compaction at 95% (was 85%) when no limit is set (#304).

### Fixed
- Chat: the "Release needed" greeting no longer invites you to ask tyci for a release; it says to release outside tyci (docs/release-workflow.md) (#289).
- Cron: a scheduled run no longer blocks the next tick, a repeating job's notice does not start a model turn in an idle chat (and carries no log tail), and a notice goes to the job that scheduled it (with the end of the log) (#316).
- Flow: `merge.sh` returns `behind` (rebase) when `ci-ok` is missing and the PR is behind the default branch, like `ci_wait.sh` (#326).
- TUI: the status line elapsed time ("bash 3.2s", "waiting for response 12.0s") keeps ticking while a picker, modal or other overlay is open (#319).
- Flow: parallel runs of one repository now merge one at a time (new `lock` state after review, then an `update` state merges the default branch and pushes, held through CI and merge), and `rebase` keeps both sides of a `CHANGELOG.md`-only conflict (#324).
- Flow: the run-finished notice says merged only after the merge step merged the PR; a stopped run says stopped, with the open PR if there is one; a skipped run says skipped (#323).
- Cost: prices and context windows of all nexos models now come from the nexos API, cached in `~/.tyci/nexos-models.json` for 6 hours and refreshed in the background (#329).
- Flow: the ask message shows the last step, its key and the last stderr line of a check, and `ci_wait.sh` prints why it returns `conflict`, `behind` or `fail` (#321).
- Flow: `ci` sends a conflicting or behind PR to `rebase` instead of `ask`, and a PR without checks no longer fails after 3 polls (#320).
- API: a provider that sends no first byte or stalls mid-stream now ends with a retryable error after `first_byte_timeout_sec` or `stream_idle_timeout_sec` (default 30 s) and the agent retries (#317).
- Orchestrator: the roadmap oracle role without `model` now uses `default_model` instead of the alias `opus` (#313).
- Flow configuration now accepts direct `provider/model` names in `default_model` and role `model` fields without requiring identity aliases.
- Flow: a failed agent state now saves its error in the `error` field of the `state.json` history entry and shows it in the ask message (#300).
- Flow: a merged issue-to-merge run now removes its worktree and branch; the run dir with `state.json` stays (#299).
- Saving TUI settings (sidebar, default model, favorites) no longer drops unknown keys (`models`, `roles`, `orchestrator`, `forge`) from `~/.tyci/config.json` (#298).

## [0.4.0] - 2026-10-07

### Added
- Config section `orchestrator` in `~/.tyci/config.json` and `.tyci/config.json`: `workers` (default 3, 0 = unlimited), `plan_timeout_sec` (default 0), `accepted_label` (default `accepted`); the project file wins key by key; invalid values stop the orchestrator start with a message naming key and file (#179)
- TUI start-up greeting with counts, plan and worker slots; starts the orchestrator on a new session; config keys `forge.kind` and `forge.repo` (#180)
- TUI chat uses its own orchestrator system prompt: only workflows, never ad-hoc process work (#181)

### Fixed
- `~/.tyci/config.json` with agent keys (`favorite_models`, `max_tokens`, `prompt_cache`, `sidebar_visible`, `auto_compact_percent`) now loads instead of failing with an unknown field error (#275)

## [0.3.0] - 2026-10-07

### Added
- Issue-to-merge workflow and runbook (`docs/dogfooding.md`). New on-disk formats: `~/.tyci/config.json` and `.tyci/config.json` (`models`, `default_model`, `roles`), `~/.tyci/worktrees/<repo>/issue-N`, `~/.tyci/runs/<repo>/<run>/state.json`, `.tyci/workflows/` and `.tyci/checks/`. New chat tools: `workflow_start`, `workflow_status`, `workflow_resume` (#175)
- `internal/worktree.AddIssue` creates fixed-path issue worktrees at `~/.tyci/worktrees/<repo>/issue-N` on branch `issue-N` from `origin/<default branch>`; `Remove` deletes only the issue leaf so sibling worktrees survive (#158)

## [0.2.0] - 2026-10-05

### Added
- `tyci --version` prints the build version: release binaries stamp the release tag, Makefile builds stamp `git describe` output (or `dev` without Git, overridable with `VERSION=`), an unstamped binary reports the main-module version the Go toolchain recorded, and a build with no version information reports `dev` (#117)
- `eventbus.Bus.SubscribeCoalesced`: a subscription that keeps only the latest event per key, so a slow consumer never loses the newest state (#113). The optional `eventbus.WithReplaces` decides which of two events with the same key is kept (#131)

### Changed
- `golangci-lint` now enforces the staticcheck style/quickfix checks ST1005, ST1011, QF1001, QF1003, QF1008, QF1011 and QF1012; the unknown-command error in TUI and interactive mode is now lowercase (#116)
- Three exported Go symbols were renamed for the ST1011 cleanup: `jobs.Job.ExtensionSeconds` → `jobs.Job.ExtensionDuration`, `tools.SubagentBackgroundAfterSec()` → `tools.SubagentBackgroundAfter()`, and `tools.SetSubagentBackgroundAfterSecForTests()` → `tools.SetSubagentBackgroundAfterForTests()`. Go callers must use the new names; no compatibility aliases were kept (#116)
- A failure to close the debug log now prints `Warning: debug log: close: ...` to stderr instead of being silently discarded; the command still succeeds, and a second close on a nil file returns nil (#118)

### Fixed
- Untrusted-project warnings for agent commands now name the skipped local cron directory and MCP configuration as well as hooks and Lua tools, using the same warning helper as workflow runs (#126)
- The TUI jobs panel no longer shows a finished job as running when a progress snapshot taken just before the job ended reaches it after the terminal event; job snapshots published to `onEvent` now carry a per-job `EventSeq`, and both the TUI's coalescing subscription and the model ignore older ones (#131)
- The TUI jobs panel no longer shows a finished job as running forever when a burst of `job.updated` events overflows the 32-slot bus while the TUI is busy; the TUI now subscribes with per-job coalescing (#113)
- The status bar now clamps its right part to the terminal width instead of relying on each right-side item to bound itself, so a future over-long right item can no longer wrap the row and break the fixed frame height (#125)
- The status bar no longer truncates the session cost mid-number on a narrow terminal: the right side's width budget is one function (`display.statusRightBudget`), and when a figure does not fit, it is dropped whole instead of being cut, so a narrow bar now loses the bill rather than showing a shortened one (#153). The left side of the bar is unchanged and still truncates from the tail
- The Go module now declares `github.com/crazy-goat/tyci-agent`, matching the repository and fixing Go module installation and dependency resolution (#115)

## [0.1.0] - 2026-10-02

First release: a CLI that runs LLM agents with a multi-turn agent loop, tool execution, session persistence, streaming responses and a TUI, configured through a JSON model registry.

### Added
- `bin/lint.sh` runs `golangci-lint fmt --diff`, `golangci-lint run`, `go vet` and shellcheck; `--fix` applies `golangci-lint fmt` first. `make lint` calls it and the CI `lint` job runs only this script with pinned golangci-lint and shellcheck (#102)
- `docs/workflow.md` and `docs/release-workflow.md` follow the shared crazy-goat templates; project commands live in the new `AGENTS.md`
- `bin/` has the shared issue and worktree helper scripts, plus `worktree-setup.sh` and `worktree-teardown.sh`
- CI: aggregate `ci-ok` check; heavy jobs are skipped for documentation-only changes; a fast `docs` job checks Markdown
- Release workflow: pushing a `vX.Y.Z` tag publishes a GitHub release with notes from this file and `tyci` binaries for linux and darwin (amd64, arm64)
- Dependabot for Go modules and GitHub Actions
- A pull request template

### Changed
- `WORKFLOW.md` is replaced by `docs/workflow.md`
- Documentation under `docs/` and `TODO.md` is translated to English; Polish remains only as test data in the subagent test plans
- The CI workflow `ci.yml` is now `tests.yaml` and reads the Go version from `go.mod`
- `.golangci.yml`: the `misspell` check no longer forces the US locale, style-only staticcheck checks (ST, QF) are not enforced, and tests are excluded from `errcheck`

### Removed
- Dead code reported by the `unused` linter

### Fixed
- Findings of `golangci-lint` (unchecked errors, deprecated `x/ansi` mode constants, empty branches, redundant conversions and unformatted files), so `lint` passes again (#102)
- Tests no longer depend on the developer's `~/.tyci`, the PTY tests work on Linux CI, and the CI test jobs pass again
