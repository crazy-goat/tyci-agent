# tyci: Provider-based Architecture

## Overview

Refactoring `tyci` to a provider-based architecture. Each provider (Zen, Anthropic, OpenAI) is a separate directory with its own implementation. On startup we iterate over the providers; each reports readiness and its list of models.

## Structure

```
tyci/
├── main.go                    # main logic, provider initialization
├── go.mod
├── providers/
│   ├── provider.go           # Provider and StreamHandler interfaces
│   ├── registry.go           # provider registry
│   ├── zen/
│   │   └── provider.go       # Zen provider implementation
│   ├── anthropic/
│   │   └── provider.go       # Anthropic provider implementation
│   └── openai/
│       └── provider.go       # OpenAI-compatible provider implementation
```

## Interfaces

### Provider Interface

```go
type Provider interface {
    Name() string              // "zen", "anthropic", "openai"
    IsConfigured() bool        // true if the API key is non-empty
    Models() []string          // e.g. ["glm-5.1", "kimi-k2.5"]
    Send(ctx context.Context, model, prompt, system string, handler StreamHandler) error
}
```

### StreamHandler Interface

```go
type StreamHandler interface {
    Chunk(text string)         // next fragment of the response
    Summary(usage UsageInfo)   // summary at the end of streaming
    End()
    Error(err error)
}

type UsageInfo struct {
    InputTokens  int
    OutputTokens int
    Cost         float64  // USD
}
```

## Provider Configuration

Each provider reads its own environment variable:

| Provider   | Env Variable    |
|------------|-----------------|
| zen        | ZEN_API_KEY     |
| anthropic  | ANTHROPIC_API_KEY |
| openai     | OPENAI_API_KEY  |

`IsConfigured()` returns `true` if the corresponding env variable is set and non-empty.

## Supported Models

| Provider   | Models                                      |
|------------|---------------------------------------------|
| zen        | glm-5.1, glm-5, kimi-k2.5, mimo-v2-pro, mimo-v2-omni |
| anthropic  | minimax-m2.7, minimax-m2.5                 |
| openai     | (brak - do rozbudowy)                      |

## CLI Behavior

### `--list` (default when no model specified)

```
Available models:
  ✓ zen/glm-5.1
  ✓ zen/kimi-k2.5
  ✓ anthropic/minimax-m2.7
  ✓ anthropic/minimax-m2.5
```

Only configured providers (IsConfigured=true) are shown.

### Model Selection

The model is given as `provider/model`:
```
tyci -m zen/glm-5.1 -p "prompt"
```

If only `model` is given without a prefix (e.g. `-m glm-5.1`), find the first provider that has it.

### Flags

- `-p`, `--prompt` string - prompt (required)
- `-s`, `--system` string - system prompt
- `-m`, `--model` string - model in the `provider/model` format (default: `zen/glm-5.1`)
- `-o`, `--output` string - output file (default: stdout)
- `--list` - list available models

## Data Flow

1. Initialization: each provider registers itself in the global registry
2. The registry builds the list of available models from the configured providers
3. The user selects a model or uses `--list`
4. `main.go` calls `provider.Send()` with the appropriate `StreamHandler`
5. The provider sends the request and streams the response via `handler.Chunk()`
6. At the end, `handler.Summary(usage)` and `handler.End()`
7. Errors via `handler.Error()`

## Implementation Notes

- Providers as separate Go packages (`package zen`, `package anthropic`, etc.)
- Shared `Provider` interface in `providers/provider.go`
- Registry in `providers/registry.go` with the function `Register(p Provider)`
- Each provider builds its own requests to its API (OpenAI-compatible or Anthropic-compatible)
- Streaming support: parsing SSE (`data:` lines)
- Graceful error handling - every error goes through `handler.Error()`
