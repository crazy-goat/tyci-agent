# tyci

**tyci** is a CLI tool that runs AI agents powered by large language models (LLMs).
It provides a multi-turn agent loop with tool execution, session persistence, streaming responses,
and a rich TUI — all configurable through a simple JSON model registry.

## Features

- **Multi-provider support** — OpenAI-compatible, Anthropic, Gemini, and custom API types
- **Agent loop** — model calls, tool execution, iteration, fallback models
- **Tool system** — built-in tools: `bash`, `find`, `read`, `write`, `todo`, `subagent`, `lua`, `wait`, `lock`, `skills`, `agents`, `web`
- **Background commands** — a shell command still running after 30s moves to the background; the agent is notified when it finishes
- **Hooks** — run your own commands before and after any tool call, to gate it or feed its result back
- **Project instructions & memory** — `AGENTS.md` plus notes the agent writes for its own future sessions
- **Prompt caching** — Anthropic cache breakpoints on the schemas, system prompt and conversation
- **`@` file completion** — type `@` in the TUI to pick a path
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

Supported wire protocols are `openai` (Chat Completions), `anthropic`,
`gemini`, and `responses`. For a per-model Responses API override, keep the
provider scheme as `openai` and add `?api=responses`; for example:

```text
openai://GPT 5.6 Luna@@api.nexos.ai/v1?api=responses&reasoning=xhigh
```

`reasoning=xhigh` is forwarded as `reasoning: {"effort":"xhigh"}` by the
Responses connector. The bare `responses://...` scheme is accepted as an alias.

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

For a model that requires Responses API, add `?api=responses` (and optionally
`&reasoning=xhigh`) to its URI in `~/.tyci/model.json`.

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
# max_iterations is accepted for compatibility but ignored; subagents are unlimited
max_iterations: 20
---

You review Go diffs. Report only defects that change behavior.
```

| Field | Effect |
|-------|--------|
| `description` | Listed in the parent's system prompt so the model knows the agent exists |
| `model` | Model the child runs on (`provider/model`); overridden by a per-call `model` |
| `tools` | Comma-separated whitelist; enforced in the child's schema *and* at call time |
| `max_iterations` | Legacy compatibility field; ignored for subagents, which run until completion or cancellation |
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

## Project instructions and memory

Two things are loaded into the system prompt at the start of every session, so
the agent does not begin each one knowing nothing but the working directory.

**`AGENTS.md`** — what you want every agent working here to know: the build
command, the test command, layering rules, what not to touch. Read from
`~/.tyci/AGENTS.md` (yours, applies everywhere) and then from the nearest
`AGENTS.md` at or above the working directory, searching no further up than the
repository root — so running `tyci` from a subdirectory still finds it.

**`.tyci/memory/*.md`** — notes the agent writes for its own future sessions
with the `memory` tool:

```
memory(action="write", name="test-command",
       content="make check runs the golden tests too; go test ./... skips them.")
memory(action="list")      # also read, delete
```

A note is for something that is *not* obvious from the code and would otherwise
have to be worked out again — a command, a rule the compiler does not enforce, a
decision and its reason. Writing the same name again corrects a note. Notes are
capped (50 files, 8 KiB each) and the whole block is capped at 32 KiB, because
all of it is re-sent on every request.

## Reply length and caching

`max_tokens` caps one reply. It defaults to a conservative 4096, because
Anthropic rejects a value above the model's own output limit and that limit
differs per model — so raise it for the models you use, either with
`--max-tokens` for one run or once in `~/.tyci/config.json`:

```json
{ "max_tokens": 16000 }
```

An agent definition can set its own with `max_tokens:` in the frontmatter.

Anthropic **prompt caching** is on by default, with cache breakpoints after the
tool schemas, after the system prompt, and at the end of the conversation — the
parts that are identical on every turn. The cache read/write counts already
appear in the usage line. Turn it off with `{"prompt_cache": false}` in
`~/.tyci/config.json` if your endpoint rejects the `cache_control` field.

## Long subagents

A blocking `subagent` call waits 60s. After that its children move to the
background — the model is notified when each finishes, exactly as with
`async=true`, and the turn ends. The point is the person at the keyboard: a
child that takes five minutes used to hold the turn open for five minutes, with
no way to type anything.

60s rather than bash's 30s because a child has a model round trip and a few
tool calls to make before it could possibly be done; a shorter window would
background almost every call. Children that finish inside the window are
returned inline as before, and a half-finished batch returns both — results for
the ones that are done, job ids for the rest.

## Long background commands

A command still running after 30s is moved to the background. At the one-minute
mark it sends one line back — how long it has been running and nothing else —
and repeats every five minutes after that.

It asks for nothing on purpose. A typo that turns a five-second command into a
hang looks exactly like a legitimately slow build from the outside, and only
the model knows which one it wrote; telling it to stop and re-check would
interrupt real work most of the time the notice fired. The age is the useful
part, so that is all the notice carries.

## Stream guards

Two things are watched on every OpenAI-compatible stream, because neither the
provider nor the agent loop notices them on its own:

- **Leaked control markers.** Some models are trained with template markers
  delimited by `｜` (DeepSeek's `<｜DSML｜parameter>` family). When a gateway
  forgets to strip them they arrive as ordinary content. They are removed
  before the text reaches the screen *or* the history — a model that sees its
  own leaked markers in the transcript produces more of them.
- **Escape sequences.** Model text and command output are arbitrary bytes drawn
  into a terminal that reads some of them as commands: `\x1b[2J` clears the
  screen, `\r` overwrites whatever was on the line, and `git diff --color` or
  `npm install` emit both. They are stripped on the way into the transcript,
  where the TUI's own colour codes are not yet mixed in.
- **A run of identical lines.** Collapsed to two copies and a count once the
  block settles, so a stuck model or a repetitive log cannot cost the whole
  viewport. Long identical lines are left alone — those are usually real data.
- **A reply cut off by `max_tokens`.** `finish_reason: "length"` is now
  reported instead of silently swallowed; a truncated tool call otherwise
  surfaced as "invalid arguments" and sent the model hunting a bug in JSON it
  had written correctly and simply not finished.
- **A model that stops making progress.** A degenerate model emits the same
  short line without end; one real session spent 78 seconds producing several
  hundred copies of `</invoke>`. After 24 consecutive identical lines the
  request is cut off, so the agent falls back to another model instead of
  paying for the loop to reach its token limit — in wall-clock, in tokens, and
  in a transcript that would carry those lines into every later request.

## Hooks

Hooks run a shell command around every tool call. They live in
`~/.tyci/hooks.json` (yours) and `./.tyci/hooks.json` (the project's); both
are loaded, global first.

```json
{
  "hooks": [
    {
      "event": "post_tool",
      "tools": ["write"],
      "paths": ["**/*.go"],
      "name": "gofmt",
      "command": "gofmt -l \"$TYCI_TOOL_PATH\""
    },
    {
      "event": "post_tool",
      "tools": ["write"],
      "paths": ["**/*.php"],
      "name": "php-lint",
      "command": "php -l \"$TYCI_TOOL_PATH\""
    },
    {
      "event": "pre_tool",
      "tools": ["write"],
      "paths": ["**/.env", "**/*.pem"],
      "name": "protect-secrets",
      "command": "echo 'refusing: that file is off limits'; exit 1"
    }
  ]
}
```

- `pre_tool` runs before the tool. A **non-zero exit blocks the call**, and
  whatever the hook printed becomes the error the model sees.
- `post_tool` runs after. What it prints is appended to the tool result, so
  the model reads it in the same turn it made the change. Add
  `"blocking": true` to also mark the result failed on a non-zero exit — the
  tool's own effects have already happened, and the message says so.

`paths` restricts a hook to the files it is actually about, which is what lets
one config serve a mixed repository: `gofmt` on `**/*.go` and `php -l` on
`**/*.php` never see each other's files. Patterns are globs — `**` crosses
directories, `*` does not, so `**/*.go` is nearly always what you want. A
hook with `paths` never fires on a call that has no path (`bash`, `find`), and
an invalid pattern is reported at startup instead of quietly matching nothing.
Omit `paths` to match every call.

Each hook receives the full call as JSON on stdin
(`{event, tool, args, success, content, error}`) plus `$TYCI_TOOL`,
`$TYCI_TOOL_PATH`, `$TYCI_TOOL_COMMAND`, `$TYCI_TOOL_SUCCESS` and
`$TYCI_HOOK_EVENT`. Omit `tools` to match every tool. Default timeout is 10s
(`"timeout"` overrides); output is capped at 8 KiB.

Hooks wrap the dispatcher, so they see MCP tools and calls made from inside a
Lua script too. A config error is reported on startup rather than ignored: a
hook that silently never fires is worse than no hook.

## `help`

`help()` lists every available tool; `help(tool="lua")` returns the long
article, worked examples included. Schema descriptions are re-sent with every
request, so they stay short — the examples that actually teach a tool live here
and cost nothing until asked for. Tools without an article fall back to their
description and parameter list, so `help` answers for MCP and `.lua` tools too.

## The `lua` tool

The agent can run a Lua script that calls other tools. It exists because every
tool call costs a request/response round trip, so anything shaped like "for
each of these N things, do X" costs N of them. A script pays one.

Inside a script:

- `tool(name, args)` → `{success, content, error}` — any tyci tool, including
  MCP tools. Goes through the same dispatcher, so hooks and the write
  freshness guard still apply.
- `log(...)` — progress, streamed live to the TUI and included in the result.
- `json_encode` / `json_decode`, and the `args` table passed in.
- `return value` hands the answer back; a table comes back as JSON.

Bounded by a wall-clock timeout (default 300s), 500 tool calls, and caps on
log and return size. `tool("lua", ...)` is refused, and a script cannot reach
a tool its agent was denied.

The script is sandboxed to the pure part of the language: `string`, `table`,
`math`, `coroutine` and the `os` clock functions. `io`, `os.execute`,
`require`, `load` and `loadfile` are removed, so the only way out of the VM is
`tool()` — which means hooks, the write freshness guard and a subagent's tool
allowlist all still apply. `print` is rebound onto `log`.

Separately, `.lua` files in `~/.tyci/tools/` or `./.tyci/tools/` are loaded as
named tools of their own.

## Write freshness

`write` refuses to modify a file that already exists unless it was read first
and has not changed since. Both write mode and edit mode replace whole files,
so writing from a stale copy silently discards someone else's work — a human
saving in their editor, a generated file, a parallel subagent. Creating a new
file and `range: "append"` need no prior read.

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
