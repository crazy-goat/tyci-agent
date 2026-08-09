# tyci

**tyci** is a CLI tool that runs AI agents powered by large language models (LLMs).
It provides a multi-turn agent loop with tool execution, session persistence, streaming responses,
and a rich TUI — all configurable through a simple JSON model registry.

## Features

- **Multi-provider support** — OpenAI-compatible, Anthropic, Gemini, and custom API types
- **Agent loop** — model calls, tool execution, iteration, fallback models
- **Tool system** — built-in tools: `bash`, `glob`, `grep`, `read`, `write`, `edit`, `todo`, `subagent`
- **Display modes** — `minimal`, `normal`, `interactive`, `tui` (Bubble Tea terminal UI)
- **Session persistence** — automatic save/resume of conversations (JSONL)
- **Streaming** — real-time thought, text, and tool output streaming
- **Agent configuration** — named agent presets with model and fallback assignments
- **Provider registration** — `tyci provider add` / `provider refresh` CLI to add or sync providers without editing JSON manually

## Installation

### Prerequisites

- Go 1.25 or later
- A terminal with true-color support (for TUI mode)

### Build from source

```bash
git clone https://github.com/crazy-goat/tyci.git
cd tyci

# Development build (with debug symbols)
make build

# Optimized release build (stripped, smaller binary)
make release

# Minimal build (exclude Anthropic and Gemini providers)
make minimal
```

The binary is named `tyci` in the current directory.

### Install to ~/local/bin

```bash
make install
```

## Quick Start

### 1. Configure a provider

On first run, `tyci` auto-fetches the provider catalog from [models.dev](https://models.dev)
and caches it to `~/.tyci/providers.json`. To pull the latest catalog at any time:

```bash
tyci provider refresh
```

For custom endpoints, add a provider manually:

```bash
tyci provider add my-provider \
  --api openai \
  --url https://api.example.com/v1 \
  --token $MY_API_KEY
```

The API key is stored in `~/.tyci/auth.json` (permissions `0600`); only model
definitions are kept in `~/.tyci/model.json`.

Or edit `~/.tyci/model.json` directly:

```json
{
  "my-provider": {
    "my-model": {
      "uri": "openai://my-model@$MY_API_KEY@api.example.com/v1"
    }
  }
}
```

URI format: `<api-type>://<model>@<token-or-env-var>@<base-url>`

Supported API types: `openai`, `anthropic`, `gemini`, `responses`.

### 2. Run the agent

```bash
# One-shot prompt
tyci run --model my-provider/my-model --prompt "What is the capital of France?"

# Interactive session
tyci console --model my-provider/my-model

# TUI mode (rich terminal UI)
tyci tui --model my-provider/my-model

# Use agent presets
tyci run --agent my-agent --prompt "Hello"
```

## Directory Layout

```
~/.tyci/
├── providers.json      # Cached models.dev provider catalog (auto-downloaded)
├── model.json          # Custom provider / model definitions (from `provider add`)
├── auth.json           # API keys per provider (permissions 0600)
├── agents.json         # Named agent configurations (name -> model + fallback)
├── agents/             # Markdown agent definitions (<name>.md, global)
│   └── .managed.json   # sha256 bookkeeping for the builtin definitions (see below)
├── history             # Readline history file
├── debug/              # Debug logs (when --no-debug is not set)
└── sessions/           # Auto-generated session files (JSONL)
```

## CLI Reference

### Commands

| Command | Description |
|---------|-------------|
| `run` | One-shot prompt (requires `--prompt`) |
| `console` | Interactive console session |
| `tui` | Rich terminal UI (Bubble Tea) |
| `agent` | Manage agent configurations |
| `provider` | Manage provider settings |
| `completion` | Generate shell completion script |

### Common Flags

These flags work with `run`, `console`, and `tui`:

| Flag | Default | Description |
|------|---------|-------------|
| `--model` | `""` | Model to use (format: `provider/model`) |
| `--agent` | `""` | Agent name for default model (from `~/.tyci/agents.json`) |
| `--max-retries` | `5` | Max retries on transient errors (0 to disable) |
| `--max-iterations` | `-1` | Max tool-call iterations (-1 = unlimited) |
| `--history-file` | `""` | Path to history file (default: `~/.tyci/history`) |
| `--session` | `""` | Session file path (default: auto-generated in `~/.tyci/sessions/`) |
| `--no-session` | `false` | Disable session persistence |
| `--debug` | `false` | Show HTTP request/response data |
| `--no-debug` | `false` | Disable API request/response debug logging |

### `run` flags

| Flag | Default | Description |
|------|---------|-------------|
| `--prompt` | `""` | **Required.** Prompt for a one-shot response |

### Subcommands

#### `tyci provider list`

List registered providers. Providers marked with `✓` are configured (have
an API key in `auth.json` or a token in their URI); otherwise they are shown
as `not configured`.

```bash
tyci provider list
```

Use `--models` to also list each provider's models:

```bash
tyci provider list --models
```

#### `tyci provider add <name>`

Add a custom provider (fetches models from the API, saves the key to
`~/.tyci/auth.json`, writes models to `~/.tyci/model.json` without tokens).

```bash
tyci provider add my-provider \
  --api openai \
  --url https://api.example.com/v1 \
  --token $MY_API_KEY \
  [--test] [--test-model <model>]
```

- `--api` — API type: `openai`, `anthropic`, `gemini`
- `--url` — API base URL
- `--token` — API key or `$ENV_VAR` reference
- `--test` — Test connectivity after adding
- `--test-model` — Model to test with (default: first model)

#### `tyci provider refresh`

Refresh the cached provider catalog from models.dev.

```bash
tyci provider refresh [--provider <id1,id2,...>] [--dry-run]
```

#### `tyci provider auth`

Manage API keys stored in `~/.tyci/auth.json`.

```bash
tyci provider auth set <provider> [<key>]
tyci provider auth get <provider>
tyci provider auth list
tyci provider auth rm <provider>
```

#### `tyci agent`

Manage named agent configurations.

```bash
tyci agent list                          # List all agents
tyci agent get <name>                    # Show agent model assignment
tyci agent set <name> <provider>/<model> # Assign model to agent
tyci agent delete <name>                 # Remove agent
tyci agent set-fallback <name> <m1> [<m2> ...]  # Set fallback models (positional)
tyci agent sync [--force]                # Unpack/update builtin agent definitions (see below)
```

#### Markdown agent definitions

Beyond the model-only presets above, an agent can be declared as a markdown file with
YAML frontmatter. The body becomes the agent's system prompt — by default *appended*
as a role on top of the standard subagent prompt (see `system_prompt_mode` below), so
you only need to describe what the agent specializes in, not restate its contract.

Definitions are read from two locations, project overriding global on name collision —
the same precedence `.tyci.json` has over `~/.tyci/agents.json`:

- `./.tyci/agents/<name>.md` — project-local, committed with the repo
- `~/.tyci/agents/<name>.md` — global

```markdown
---
description: Reviews Go diffs for correctness
model: anthropic/claude-sonnet-5
tools: read, find, bash
max_iterations: 20
---

You review Go diffs. Report only defects that change behavior.
```

| Field | Effect |
|-------|--------|
| `description` | Listed in the parent's system prompt so the model knows the agent exists |
| `model` | Model the child runs on (`provider/model`); overridden by a per-call `model` |
| `tools` | Comma-separated whitelist; enforced in the child's schema *and* at call time |
| `max_iterations` | Caps the child's tool-call turns; overridden by a per-call `max_iterations` |
| `temperature` | Sampling temperature, `0`–`2`. `0` is a real value ("deterministic"), not "unset" |
| `fallback` | Models tried in order when the primary fails; an unresolvable spec is skipped, never fatal |
| `system` | Optional — overrides the markdown body as the *source text* used for the system prompt (still subject to `system_prompt_mode` below) |
| `system_prompt_mode` | `append` (default) or `replace` — see below |

`system_prompt_mode` controls how the body (or `system`, if set) is combined with the
standard subagent system prompt (contract, date/cwd/OS, tool descriptions, the
project's `AGENTS.md`, available skills):

- `append` (default): the body is a **role** layered on top of that standard prompt —
  the agent keeps the subagent contract and every bit of environment context, and only
  needs to describe its specialization.
- `replace`: the body **is** the entire system prompt, verbatim. Full control, but full
  responsibility for restating anything the agent needs — including the subagent
  contract and `AGENTS.md` — since none of it is added automatically.

Omitting `system_prompt_mode` is equivalent to `append`. This is a deliberate behavior
change: earlier versions always replaced the whole prompt with the body, which silently
dropped the project's `AGENTS.md` and the subagent contract — harmful for an agent that
writes to the repo. Definitions that relied on full replacement must now set
`system_prompt_mode: replace` explicitly.

Invoke one with the `subagent` tool: `subagent(agent: "reviewer", task: "...")`.
An unknown agent name is an error, not a silent fallback. `subagent` is never granted
to a child, even if listed in `tools` — subagents cannot spawn subagents.

A `temperature` outside `0`–`2` makes the whole definition unparseable, and unparseable
definitions are skipped silently — the agent simply will not appear in the list.
Anthropic accepts only `0`–`1`; the narrower limit is enforced by its API, not here,
since a definition does not know which provider it will ultimately run on.

##### Builtin agents

tyci ships three ready-to-use definitions baked into the binary — `locator`, `reviewer`,
`implementer` — so `subagent(agent: "locator", ...)` works with no setup. On every
startup tyci unpacks them into `~/.tyci/agents/`, updating any that changed in a newer
tyci release, **but only for files it can prove it last wrote and you have not touched**:

- Never edited a builtin file? It gets updated automatically when you upgrade tyci.
- Edited it yourself (even a whitespace change)? That freezes it — permanently. tyci
  will never overwrite it again on its own, so your edit is safe across every future
  upgrade. Copy it to `.tyci/agents/` (project-local) if you want your own version to
  win over a future global one instead.
- Deleted it? That is respected as a deliberate choice, not resurrected on the next run.
  Bring it back with `tyci agent sync --force`, which also overwrites any local edits —
  see `tyci agent sync --help` for the full explanation.

The builtin definitions deliberately omit `model`, so they inherit whatever model the
parent agent is running on and work unmodified with every provider — nothing to
configure to try them.

### Display Modes

- **minimal** — Plain text output, no decorations
- **normal** — Colored terminal output with basic formatting
- **interactive** — Full interactive readline session (line editing, history)
- **tui** — Bubble Tea TUI with split-pane, model picker, mouse support

#### `tyci completion`

Generate shell completion script (`bash`, `zsh`, `fish`, `powershell`).

```bash
source <(tyci completion bash)
# or add to .bashrc:
tyci completion bash > /etc/bash_completion.d/tyci
```

## Session Management

Sessions are automatically saved to `~/.tyci/sessions/` as JSONL files.
Each line is a complete event (message, tool call, result, usage).

- Re-run with `--session <path>` to resume a previous session
- Session replay shows history before continuing
- Use `--no-session` to disable persistence

## Security Notes

- API tokens are stored in `~/.tyci/auth.json` with permissions `0600`. Protect
  this file with appropriate file permissions.
- The `bash` tool executes shell commands. Review tool call arguments
  before approving if you enable tool confirmation.
- Environment variables referenced in URIs (e.g. `$MY_API_KEY`) are
  expanded at runtime — the raw variable name is stored, not the value.
- Debug logs in `~/.tyci/debug/` may contain API request/response data.
  Clean them if you share your machine.

## Development

### Project Structure

```
tyci/
├── agent/            # Agent loop, message management, iteration logic
├── api/              # HTTP client, retry logic, streaming helpers
├── display/          # Display interfaces and implementations (terminal, TUI)
├── docs/             # Planning documents and specifications
├── internal/
│   ├── connect/      # Provider registration via CLI
│   ├── debug/        # Debug logging infrastructure
│   └── readline/     # Line editing and history
├── providers/        # LLM provider implementations and model registry
├── session/          # Session persistence (JSONL save/resume)
├── stream/           # Streaming types (Usage, Stats, event handling)
├── tools/            # Tool implementations (bash, glob, grep, read, write, etc.)
├── main.go           # CLI entry point and flag handling
└── Makefile          # Build targets (build, release, minimal)
```

### Running Tests

```bash
# All tests
go test ./... -count=1

# With race detection
go test -race ./... -count=1

# Static analysis
go vet ./...
```

### Code Style

This project follows standard Go conventions. Run `gofmt` before committing:

```bash
gofmt -l -w .
```

### Build Variants

```bash
make build     # Debug build (with debug symbols)
make release   # Release build (stripped, optimized)
make minimal   # Minimal build (exclude some providers via build tags)
```

## License

MIT — see [LICENSE](./LICENSE).
