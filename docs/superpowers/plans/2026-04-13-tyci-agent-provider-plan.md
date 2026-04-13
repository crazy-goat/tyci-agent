# tyci-agent Provider Architecture - Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refaktor `tyci-agent` na provider-based architecture z osobnymi providerami dla Zen, Anthropic i OpenAI.

**Architecture:** Każdy provider jest osobnym pakietem Go w `providers/<name>/`. Wspólny interfejs `Provider` definiuje zachowanie. Na starcie rejestr iteruje po providerach i buduje listę dostępnych modeli.

**Tech Stack:** Go 1.24, net/http, encoding/json

---

## File Structure

```
tyci-agent/
├── main.go                          # refactor: use providers
├── go.mod                           # unchanged
├── providers/
│   ├── provider.go                  # CREATE: interfaces
│   ├── registry.go                  # CREATE: provider registry
│   ├── zen/
│   │   └── provider.go              # CREATE: Zen implementation
│   ├── anthropic/
│   │   └── provider.go              # CREATE: Anthropic implementation
│   └── openai/
│       └── provider.go              # CREATE: OpenAI implementation (placeholder)
```

---

## Task 1: Create provider interfaces

**Files:**
- Create: `providers/provider.go`

- [ ] **Step 1: Write interfaces**

```go
package providers

type UsageInfo struct {
    InputTokens  int
    OutputTokens int
    Cost         float64
}

type StreamHandler interface {
    Chunk(text string)
    Summary(usage UsageInfo)
    End()
    Error(err error)
}

type Provider interface {
    Name() string
    IsConfigured() bool
    Models() []string
    Send(ctx context.Context, model, prompt, system string, handler StreamHandler) error
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/provider.go
git commit -m "feat: add Provider and StreamHandler interfaces"
```

---

## Task 2: Create provider registry

**Files:**
- Create: `providers/registry.go`

- [ ] **Step 1: Write registry**

```go
package providers

import (
    "context"
    "fmt"
    "os"
    "strings"
)

var (
    providers = make(map[string]Provider)
)

func Register(p Provider) {
    providers[p.Name()] = p
}

func ListProviders() []Provider {
    var result []Provider
    for _, p := range providers {
        result = append(result, p)
    }
    return result
}

func GetProvider(name string) (Provider, bool) {
    p, ok := providers[name]
    return p, ok
}

func FindModel(model string) (Provider, string, bool) {
    if strings.Contains(model, "/") {
        parts := strings.SplitN(model, "/", 2)
        if p, ok := providers[parts[0]]; ok {
            return p, parts[1], true
        }
        return nil, "", false
    }
    for _, p := range providers {
        if !p.IsConfigured() {
            continue
        }
        for _, m := range p.Models() {
            if m == model {
                return p, model, true
            }
        }
    }
    return nil, "", false
}

type DefaultHandler struct {
    output   *strings.Builder
    done     chan struct{}
}

func NewDefaultHandler() *DefaultHandler {
    return &DefaultHandler{
        output: new(strings.Builder),
        done:   make(chan struct{}),
    }
}

func (h *DefaultHandler) Chunk(text string) {
    fmt.Print(text)
    h.output.WriteString(text)
}

func (h *DefaultHandler) Summary(usage UsageInfo) {
    fmt.Fprintf(os.Stderr, "\n[Tokens: %d in / %d out, Cost: $%.6f]\n",
        usage.InputTokens, usage.OutputTokens, usage.Cost)
}

func (h *DefaultHandler) End() {
    close(h.done)
}

func (h *DefaultHandler) Error(err error) {
    fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}

func (h *DefaultHandler) Done() <-chan struct{} {
    return h.done
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/registry.go
git commit -m "feat: add provider registry with FindModel and DefaultHandler"
```

---

## Task 3: Create Zen provider

**Files:**
- Create: `providers/zen/provider.go`

- [ ] **Step 1: Write Zen provider**

```go
package zen

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strings"

    "github.com/decodo/tyci-agent/providers"
)

const baseURL = "https://opencode.ai/zen/go"

type provider struct{}

func init() {
    providers.Register(&provider{})
}

func (p *provider) Name() string {
    return "zen"
}

func (p *provider) IsConfigured() bool {
    key := os.Getenv("ZEN_API_KEY")
    return key != ""
}

func (p *provider) Models() []string {
    return []string{"glm-5.1", "glm-5", "kimi-k2.5", "mimo-v2-pro", "mimo-v2-omni"}
}

type message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type requestBody struct {
    Model    string    `json:"model"`
    Stream   bool      `json:"stream"`
    Messages []message `json:"messages"`
}

type streamChunk struct {
    Choices []struct {
        Delta struct {
            Content string `json:"content"`
        } `json:"delta"`
    } `json:"choices"`
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
    apiKey := os.Getenv("ZEN_API_KEY")
    if apiKey == "" {
        return fmt.Errorf("ZEN_API_KEY not set")
    }

    body := requestBody{
        Model:  model,
        Stream: true,
        Messages: []message{},
    }
    if system != "" {
        body.Messages = append(body.Messages, message{Role: "system", Content: system})
    }
    body.Messages = append(body.Messages, message{Role: "user", Content: prompt})

    jsonBody, err := json.Marshal(body)
    if err != nil {
        return err
    }

    req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/chat/completions", strings.NewReader(string(jsonBody)))
    if err != nil {
        return err
    }

    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "text/event-stream")

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(bodyBytes))
    }

    reader := bufio.NewReader(resp.Body)
    for {
        line, err := reader.ReadString('\n')
        if err != nil {
            break
        }
        line = strings.TrimSpace(line)
        if line == "" || !strings.HasPrefix(line, "data:") {
            continue
        }

        data := strings.TrimPrefix(line, "data: ")
        if data == "[DONE]" {
            break
        }

        var chunk streamChunk
        if err := json.Unmarshal([]byte(data), &chunk); err != nil {
            continue
        }
        if len(chunk.Choices) > 0 {
            content := chunk.Choices[0].Delta.Content
            if content != "" {
                handler.Chunk(content)
            }
        }
    }

    handler.Summary(providers.UsageInfo{})
    handler.End()
    return nil
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/zen/provider.go
git commit -m "feat: add Zen provider implementation"
```

---

## Task 4: Create Anthropic provider

**Files:**
- Create: `providers/anthropic/provider.go`

- [ ] **Step 1: Write Anthropic provider**

```go
package anthropic

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"

    "github.com/decodo/tyci-agent/providers"
)

const baseURL = "https://opencode.ai/zen/go"

type provider struct{}

func init() {
    providers.Register(&provider{})
}

func (p *provider) Name() string {
    return "anthropic"
}

func (p *provider) IsConfigured() bool {
    key := os.Getenv("ANTHROPIC_API_KEY")
    return key != ""
}

func (p *provider) Models() []string {
    return []string{"minimax-m2.7", "minimax-m2.5"}
}

type message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}

type requestBody struct {
    Model     string    `json:"model"`
    Stream    bool      `json:"stream"`
    MaxTokens int       `json:"max_tokens"`
    Messages  []message `json:"messages"`
}

type chunk struct {
    Type string `json:"type"`
    Delta struct {
        Text string `json:"text"`
    } `json:"delta"`
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
    apiKey := os.Getenv("ANTHROPIC_API_KEY")
    if apiKey == "" {
        return fmt.Errorf("ANTHROPIC_API_KEY not set")
    }

    body := requestBody{
        Model:     model,
        Stream:    true,
        MaxTokens: 4096,
        Messages:  []message{},
    }
    if system != "" {
        body.Messages = append(body.Messages, message{Role: "user", Content: prompt})
    } else {
        body.Messages = append(body.Messages, message{Role: "user", Content: prompt})
    }

    jsonBody, err := json.Marshal(body)
    if err != nil {
        return err
    }

    req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/v1/messages", strings.NewReader(string(jsonBody)))
    if err != nil {
        return err
    }

    req.Header.Set("x-api-key", apiKey)
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Accept", "text/event-stream")
    req.Header.Set("anthropic-version", "2023-06-01")

    client := &http.Client{}
    resp, err := client.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()

    if resp.StatusCode != 200 {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(bodyBytes))
    }

    reader := bufio.NewReader(resp.Body)
    for {
        line, err := reader.ReadString('\n')
        if err != nil {
            break
        }
        line = strings.TrimSpace(line)
        if line == "" || !strings.HasPrefix(line, "data:") {
            continue
        }

        data := strings.TrimPrefix(line, "data: ")
        if data == "[DONE]" {
            break
        }

        var c chunk
        if err := json.Unmarshal([]byte(data), &c); err != nil {
            continue
        }
        if c.Type == "content_block_delta" {
            if c.Delta.Text != "" {
                handler.Chunk(c.Delta.Text)
            }
        }
    }

    handler.Summary(providers.UsageInfo{})
    handler.End()
    return nil
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/anthropic/provider.go
git commit -m "feat: add Anthropic provider implementation"
```

---

## Task 5: Create OpenAI provider (placeholder)

**Files:**
- Create: `providers/openai/provider.go`

- [ ] **Step 1: Write OpenAI provider placeholder**

```go
package openai

import (
    "context"
    "fmt"
    "os"

    "github.com/decodo/tyci-agent/providers"
)

type provider struct{}

func init() {
    providers.Register(&provider{})
}

func (p *provider) Name() string {
    return "openai"
}

func (p *provider) IsConfigured() bool {
    key := os.Getenv("OPENAI_API_KEY")
    return key != ""
}

func (p *provider) Models() []string {
    return []string{}
}

func (p *provider) Send(ctx context.Context, model, prompt, system string, handler providers.StreamHandler) error {
    handler.Error(fmt.Errorf("openai provider not implemented"))
    handler.End()
    return nil
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/openai/provider.go
git commit -m "feat: add OpenAI provider placeholder"
```

---

## Task 6: Refactor main.go

**Files:**
- Modify: `main.go` - complete rewrite to use providers

- [ ] **Step 1: Write new main.go**

```go
package main

import (
    "context"
    "flag"
    "fmt"
    "os"
    "os/signal"
    "strings"
    "syscall"

    "github.com/decodo/tyci-agent/providers"
    _ "github.com/decodo/tyci-agent/providers/zen"
    _ "github.com/decodo/tyci-agent/providers/anthropic"
    _ "github.com/decodo/tyci-agent/providers/openai"
)

func main() {
    prompt := flag.String("p", "", "prompt (required)")
    system := flag.String("s", "", "system prompt")
    model := flag.String("m", "", "model in format provider/model (e.g., zen/glm-5.1)")
    output := flag.String("o", "stdout", "output file (default: stdout)")
    listFlag := flag.Bool("list", false, "list available models")
    flag.Parse()

    if *listFlag || *model == "" {
        listModels()
        os.Exit(0)
    }

    if *prompt == "" {
        fmt.Fprintln(os.Stderr, "Error: --prompt is required")
        os.Exit(1)
    }

    p, modelName, ok := providers.FindModel(*model)
    if !ok {
        fmt.Fprintf(os.Stderr, "Error: unknown model: %s (use --list to see available)\n", *model)
        os.Exit(1)
    }

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-sigChan
        cancel()
    }()

    var out *os.File
    if *output != "stdout" {
        var err error
        out, err = os.Create(*output)
        if err != nil {
            fmt.Fprintf(os.Stderr, "Error creating file: %v\n", err)
            os.Exit(1)
        }
        defer out.Close()
    } else {
        out = os.Stdout
    }

    handler := &writerHandler{out: out}
    if err := p.Send(ctx, modelName, *prompt, *system, handler); err != nil {
        fmt.Fprintf(os.Stderr, "Error: %v\n", err)
        os.Exit(1)
    }
}

func listModels() {
    fmt.Println("Available models:")
    for _, p := range providers.ListProviders() {
        if !p.IsConfigured() {
            continue
        }
        for _, m := range p.Models() {
            fmt.Printf("  ✓ %s/%s\n", p.Name(), m)
        }
    }
}

type writerHandler struct {
    out *os.File
}

func (h *writerHandler) Chunk(text string) {
    fmt.Fprint(h.out, text)
    h.out.Sync()
}

func (h *writerHandler) Summary(usage providers.UsageInfo) {
    if usage.InputTokens > 0 || usage.OutputTokens > 0 {
        fmt.Fprintf(os.Stderr, "\n[Tokens: %d in / %d out, Cost: $%.6f]\n",
            usage.InputTokens, usage.OutputTokens, usage.Cost)
    }
}

func (h *writerHandler) End() {}

func (h *writerHandler) Error(err error) {
    fmt.Fprintf(os.Stderr, "Error: %v\n", err)
}
```

- [ ] **Step 2: Build and test**

```bash
cd /home/decodo/work/tyci-agent && go build -o tyci-agent .
./tyci-agent --list
```

Expected output:
```
Available models:
```

(ponieważ żaden API key nie jest ustawiony)

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "refactor: use provider architecture"
```

---

## Task 7: Add imports to main.go

**Files:**
- Modify: `main.go` - add bufio import

- [ ] **Step 1: Check and fix imports**

Current main.go needs `bufio` import for bufio.NewReader in providers. Check if Go build passes:

```bash
cd /home/decodo/work/tyci-agent && go build -o tyci-agent .
```

If there are issues, fix them.

- [ ] **Step 2: Commit any fixes**

```bash
git add main.go && git commit -m "fix: ensure all imports in main.go"
```

---

## Verification

After all tasks:

```bash
cd /home/decodo/work/tyci-agent && go build -o tyci-agent . && ./tyci-agent --list
go vet ./...
go fmt ./...
```

Expected: builds successfully, `--list` shows available models (empty if no API keys set).

---

## Implementation Order

1. provider.go (interfaces)
2. registry.go (registry + DefaultHandler)
3. zen/provider.go
4. anthropic/provider.go
5. openai/provider.go (placeholder)
6. main.go (use providers)
