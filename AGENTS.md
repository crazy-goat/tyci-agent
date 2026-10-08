# AGENTS.md

Project commands and specifics for tyci-agent. The development process (issue,
worktree, review, PR, merge) is in [docs/workflow.md](docs/workflow.md), the release
process in [docs/release-workflow.md](docs/release-workflow.md).

Everything is written in English (code, comments, docs, commits, issues).

tyci-agent is a CLI that runs LLM-powered agents (binary name `tyci`). It is a single Go
module, `github.com/crazy-goat/tyci-agent`, with the `main` package at the repository root.
macOS and Linux only; Windows is not a supported target (unix-only syscalls).

## Layout

| Path | Content |
|---|---|
| `main.go`, `commands.go`, `*cmd.go` | CLI entry point and cobra commands |
| `agent/` | Agent loop, retries, fallback, tool execution |
| `api/`, `connector/`, `providers/` | Wire protocols, model clients and the provider catalog |
| `tools/` | Built-in tools (bash, read, write, edit, subagent, wait, lua, MCP, ...) |
| `display/` | TUI, console and minimal frontends |
| `session/`, `jobs/`, `locks/`, `eventbus/`, `stream/` | Sessions, background jobs, locks, events, streaming |
| `internal/` | MCP client, cron, hooks, skills, readline, workflow runner (`flow/`), config |
| `internal/agentdefs/builtin/*.md` | Agent definitions embedded in the binary (code, not docs) |
| `docs/` | Design notes, test plans and process docs |
| `TODO.md` | Backlog notes that source comments refer to |

Read `README.md` before changing commands, flags or file formats. They are the public interface.

## Commands

Go version: from `go.mod`.

```bash
go build ./...                          # compile everything
go test ./... -count=1                  # unit tests
go test -race ./... -count=1            # race detector (important for concurrent code)
go test -tags "noanthropic,nogemini" ./... -count=1   # build without the Anthropic and Gemini clients
bin/lint.sh                             # gofmt, golangci-lint (.golangci.yml, v2), go vet, shellcheck; check only
bin/lint.sh --fix                       # golangci-lint fmt first, then the same checks
make lint                               # same as bin/lint.sh

make build                              # debug binary ./tyci
make release                            # stripped binary ./tyci
make minimal                            # stripped binary without Anthropic and Gemini
make install                            # copy to ~/local/bin
```

`golangci-lint` v2 and `shellcheck` must be installed locally (CI pins golangci-lint v2.13.2
and shellcheck v0.11.0). A missing tool fails `bin/lint.sh`.

The repository has no Docker Compose stack and tests open no fixed ports, so
`bin/worktree-setup.sh` only downloads Go modules and `bin/worktree-teardown.sh` does nothing.

Generated binaries (`/tyci`, `/dist/`) must never be committed.

## CI

`.github/workflows/tests.yaml` runs on pull requests and pushes to `main`. The `changes`
job detects documentation-only changes; the `docs` job checks them fast. The `lint`
job (`bin/lint.sh`), `test` (Linux and macOS, with and without `-race`) and `test-tagged`
run only for code changes. The required check is `ci-ok`. Pushing a `vX.Y.Z` tag runs
`.github/workflows/release.yaml`, which attaches `tyci` binaries for Linux and macOS.

## Conventions

- Commit scopes: `agent`, `api`, `connector`, `providers`, `tools`, `display`, `session`,
  `jobs`, `mcp`, `cron`, `cli`, `docs`, `ci`, `workflow`, `checks`, `roles`, `config`,
  `chat`, `worktree`.
  Example: `fix(tools): close the response body on cancel (#12)`.
- Commands, flags and the on-disk formats under `~/.tyci/` are a public interface. Change them
  deliberately and document the change in `CHANGELOG.md` and `README.md`.
- `errcheck` stays on. Handle errors that change behaviour; discard best-effort ones explicitly
  with `_ =`. Do not add `//nolint` without a reason.
- `unused` stays on: delete dead code instead of keeping it "for later".
- New code gets tests. Concurrency-related changes need to pass `go test -race`.
- Milestone numbers are not versions. Use the milestone title (`vX.Y.Z`).
