# tyci-agent: Provider-based Architecture

## Overview

Refaktoryzacja `tyci-agent` na architekturę provider-based. Każdy provider (Zen, Anthropic, OpenAI) jest osobnym katalogem z własną implementacją. Na starcie iterujemy po providerach, każdy zgłasza gotowość i listę modeli.

## Structure

```
tyci-agent/
├── main.go                    # główna logika, inicjalizacja providerów
├── go.mod
├── providers/
│   ├── provider.go           # interfejsy Provider i StreamHandler
│   ├── registry.go           # rejestr providerów
│   ├── zen/
│   │   └── provider.go       # implementacja Zen provider
│   ├── anthropic/
│   │   └── provider.go       # implementacja Anthropic provider
│   └── openai/
│       └── provider.go       # implementacja OpenAI-compatible provider
```

## Interfaces

### Provider Interface

```go
type Provider interface {
    Name() string              // "zen", "anthropic", "openai"
    IsConfigured() bool        // true jeśli API key jest niepusty
    Models() []string          // np. ["glm-5.1", "kimi-k2.5"]
    Send(ctx context.Context, model, prompt, system string, handler StreamHandler) error
}
```

### StreamHandler Interface

```go
type StreamHandler interface {
    Chunk(text string)         // kolejny fragment odpowiedzi
    Summary(usage UsageInfo)   // podsumowanie na końcu streamingu
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

Każdy provider odczytuje swoją zmienną środowiskową:

| Provider   | Env Variable    |
|------------|-----------------|
| zen        | ZEN_API_KEY     |
| anthropic  | ANTHROPIC_API_KEY |
| openai     | OPENAI_API_KEY  |

`IsConfigured()` zwraca `true` jeśli odpowiednia zmienna env jest ustawiona i niepusta.

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

Tylko skonfigurowane (IsConfigured=true) providery są pokazywane.

### Model Selection

Model podawany jako `provider/model`:
```
tyci-agent -m zen/glm-5.1 -p "prompt"
```

Jeśli podany tylko `model` bez prefixu (np. `-m glm-5.1`), wyszukaj pierwszego providera który go ma.

### Flags

- `-p`, `--prompt` string - prompt (required)
- `-s`, `--system` string - system prompt
- `-m`, `--model` string - model w formacie `provider/model` (default: `zen/glm-5.1`)
- `-o`, `--output` string - output file (default: stdout)
- `--list` - list available models

## Data Flow

1. Inicjalizacja: każdy provider rejestruje się w globalnym rejestrze
2. Rejestr buduje listę dostępnych modeli z skonfigurowanych providerów
3. Użytkownik wybiera model lub używa `--list`
4. `main.go` wywołuje `provider.Send()` z odpowiednim `StreamHandler`
5. Provider wysyła request, streaming odpowiedzi przez `handler.Chunk()`
6. Na końcu `handler.Summary(usage)` i `handler.End()`
7. Błędy przez `handler.Error()`

## Implementation Notes

- Providerzy jako osobne paczki Go (`package zen`, `package anthropic`, etc.)
- Wspólny interfejs `Provider` w `providers/provider.go`
- Rejestr w `providers/registry.go` z funkcją `Register(p Provider)`
- Każdy provider sam tworzy requesty do swojego API (OpenAI-compatible lub Anthropic-compatible)
- Obsługa streaming: parsowanie SSE (`data:` lines)
- Graceful error handling - każdy błąd idzie przez `handler.Error()`
