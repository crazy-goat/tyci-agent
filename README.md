# tyci

**tyci** is a CLI tool that runs AI agents powered by large language models (LLMs).
It provides a multi-turn agent loop with tool execution, session persistence, streaming responses,
and a rich TUI — all configurable through a simple JSON model registry.

## Features

- **Multi-provider support** — OpenAI-compatible, Anthropic, Gemini, and custom API types
- **Agent loop** — model calls, tool execution, iteration, fallback models
- **Tool system** — built-in tools: `bash`, `glob`, `grep`, `read`, `write`, `edit`, `todo`, `subagent`
- **Run modes** — `run` (one-shot, minimal), `console` (interactive REPL), `tui` (Bubble Tea UI)
- **Session persistence** — automatic save/resume of conversations (JSONL)
- **Streaming** — real-time thought, text, and tool output streaming
- **Agent configuration** — named agent presets with model and fallback assignments
- **Provider registration** — `tyci connect` CLI to add providers without editing JSON manually

## Installation

### Prerequisites

- Go 1.25 or later
- A terminal with true-color support (for TUI mode)

### Build from source

```bash
git clone https://github.com/crazy-goat/tyci-agent.git
cd tyci-agent

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

Providers are defined in `~/.tyci/model.json`. Each provider contains models
with a URI scheme that specifies the API type, model name, authentication,
and base URL.

Example using the `connect` subcommand:

```bash
tyci connect \
  --name my-provider \
  --api openai \
  --url https://api.example.com/v1 \
  --token $MY_API_KEY
```

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
# One-shot prompt (minimal display)
tyci run --model my-provider/my-model --prompt "What is the capital of France?"

# Interactive REPL with history and slash commands
tyci console --model my-provider/my-model

# Rich TUI (Bubble Tea)
tyci tui --model my-provider/my-model

# Use agent presets
tyci run --agent my-agent --prompt "Hello"
```

## Directory Layout

```
~/.tyci/
├── model.json          # Provider and model definitions
├── agents.json         # Named agent configurations
├── history             # Readline history file
├── debug/              # Debug logs (when --no-debug is not set)
└── sessions/           # Auto-generated session files (JSONL)
```

## CLI Reference

### Subcommands

| Subcommand | Description |
|------------|-------------|
| `tyci run` | One-shot run with a single `--prompt` (minimal display) |
| `tyci console` | Interactive REPL with readline, history, slash commands |
| `tyci tui` | Bubble Tea TUI with model picker, split-pane, mouse support |
| `tyci agent` | Manage agent configurations (list/get/set/delete/set-fallback) |
| `tyci provider` | Manage providers and auth (add/refresh/auth) |

### Common Flags (run, console, tui)

| Flag | Default | Description |
|------|---------|-------------|
| `--model` | `""` | Model to use (format: `provider/model`) |
| `--agent` | `""` | Agent name for default model (from `~/.tyci/agents.json`) |
| `--prompt` | `""` | Prompt for a one-shot response (required for `run`) |
| `--max-retries` | `5` | Max retries on transient errors (0 to disable) |
| `--max-iterations` | `-1` | Max tool-call iterations (-1 = unlimited) |
| `--history-file` | `""` | Path to history file (default: `~/.tyci/history`) |
| `--session` | `""` | Session file path (default: auto-generated in `~/.tyci/sessions/`) |
| `--no-session` | `false` | Disable session persistence |
| `--debug` | `false` | Show HTTP request/response data |
| `--no-debug` | `false` | Disable API request/response debug logging |

#### `tyci agent`

Manage named agent configurations.

```bash
tyci agent list                          # List all agents
tyci agent get <name>                    # Show agent model assignment
tyci agent set <name> --model <model>    # Assign model to agent
tyci agent delete <name>                 # Remove agent
tyci agent set-fallback <name> --model <m1> [--model <m2> ...]  # Set fallback models
```

## Session Management

Sessions are automatically saved to `~/.tyci/sessions/` as JSONL files.
Each line is a complete event (message, tool call, result, usage).

- Re-run with `--session <path>` to resume a previous session
- Session replay shows history before continuing
- Use `--no-session` to disable persistence

## Security Notes

- API tokens are stored in `~/.tyci/model.json`. Protect this file with
  appropriate file permissions (`chmod 600`).
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
