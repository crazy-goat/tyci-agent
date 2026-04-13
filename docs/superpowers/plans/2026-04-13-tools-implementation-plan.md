# Tools Implementation Plan

> **For agentic workers:** Implementacja task po tasku bez subagentów - proste pliki.

**Goal:** Dodanie tooli (read, write, edit, bash) do tyci-agent z wymianą przez stdin/stdout pipe.

**Architecture:** CLI przyjmuje JSON z stdin, wykonuje tool, zwraca JSON przez stdout. Tool wywoływany przez AI jako subprocess.

**Tech Stack:** Go 1.24, os/exec, encoding/json

---

## File Structure

```
tyci-agent/
├── main.go           # rozszerzony o tool execution
├── tools/
│   ├── tool.go     # interfejs Tool
│   ├── read.go    # plik read
│   ├── write.go   # plik write
│   ├── edit.go   # plik edit
│   └── bash.go   # shell command
```

---

## Task 1: Create tool interface

**Files:**
- Create: `tools/tool.go`

- [ ] **Step 1: Write interfaces**

```go
package tools

type ToolResult struct {
    Type    string `json:"type"`
    Success bool   `json:"success"`
    Content string `json:"content,omitempty"`
    Error   string `json:"error,omitempty"`
}

type Tool interface {
    Name() string
    Run(input map[string]any) ToolResult
}
```

- [ ] **Step 2: Commit**

```bash
git add tools/tool.go && git commit -m "feat: add Tool interface"
```

---

## Task 2: Create read tool

**Files:**
- Create: `tools/read.go`

- [ ] **Step 1: Write read tool**

```go
package tools

import (
    "encoding/json"
    "os"
    "io"
)

type ReadTool struct{}

func (t *ReadTool) Name() string { return "read" }

func (t *ReadTool) Run(input map[string]any) ToolResult {
    path, ok := input["path"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "path required"}
    }

    data, err := os.ReadFile(path)
    if err != nil {
        return ToolResult{Type: "result", Success: false, Error: err.Error()}
    }
    return ToolResult{Type: "result", Success: true, Content: string(data)}
}
```

- [ ] **Step 2: Commit**

```bash
git add tools/read.go && git commit -m "feat: add Read tool"
```

---

## Task 3: Create write tool

**Files:**
- Create: `tools/write.go`

- [ ] **Step 1: Write write tool**

```go
package tools

import (
    "os"
)

type WriteTool struct{}

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Run(input map[string]any) ToolResult {
    path, ok := input["path"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "path required"}
    }

    content, ok := input["content"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "content required"}
    }

    err := os.WriteFile(path, []byte(content), 0644)
    if err != nil {
        return ToolResult{Type: "result", Success: false, Error: err.Error()}
    }
    return ToolResult{Type: "result", Success: true, Content: "written " + path}
}
```

- [ ] **Step 2: Commit**

```bash
git add tools/write.go && git commit -m "feat: add Write tool"
```

---

## Task 4: Create edit tool

**Files:**
- Create: `tools/edit.go`

- [ ] **Step 1: Write edit tool**

```go
package tools

import (
    "os"
    "strings"
)

type EditTool struct{}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Run(input map[string]any) ToolResult {
    path, ok := input["path"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "path required"}
    }

    old, ok := input["oldString"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "oldString required"}
    }

    new, ok := input["newString"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "newString required"}
    }

    data, err := os.ReadFile(path)
    if err != nil {
        return ToolResult{Type: "result", Success: false, Error: err.Error()}
    }

    content := strings.Replace(string(data), old, new, 1)
    err = os.WriteFile(path, []byte(content), 0644)
    if err != nil {
        return ToolResult{Type: "result", Success: false, Error: err.Error()}
    }
    return ToolResult{Type: "result", Success: true, Content: "edited " + path}
}
```

- [ ] **Step 2: Commit**

```bash
git add tools/edit.go && git commit -m "feat: add Edit tool"
```

---

## Task 5: Create bash tool

**Files:**
- Create: `tools/bash.go`

- [ ] **Step 1: Write bash tool**

```go
package tools

import (
    "os/exec"
    "bytes"
)

type BashTool struct{}

func (t *BashTool) Name() string { return "bash" }

func (t *BashTool) Run(input map[string]any) ToolResult {
    cmd, ok := input["command"].(string)
    if !ok {
        return ToolResult{Type: "result", Success: false, Error: "command required"}
    }

    parts := strings.Fields(cmd)
    c := exec.Command(parts[0], parts[1:]...)
    var out bytes.Buffer
    c.Stdout = &out
    c.Stderr = &out
    err := c.Run()
    if err != nil {
        return ToolResult{Type: "result", Success: false, Error: out.String()}
    }
    return ToolResult{Type: "result", Success: true, Content: out.String()}
}
```

- [ ] **Step 2: Commit**

```bash
git add tools/bash.go && git commit -m "feat: add Bash tool"
```

---

## Task 6: Integrate tools w main.go

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Update main.go**

```go
package main

import (
    "encoding/json"
    "os"
    "fmt"

    "github.com/decodo/tyci-agent/tools"
)

func main() {
    var input struct {
        Type string         `json:"type"`
        Input map[string]any `json:"input,omitempty"`
    }
    if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
        json.NewEncoder(os.Stdout).Encode(tools.ToolResult{
            Type: "result", Success: false, Error: err.Error()})
        os.Exit(1)
    }

    var result tools.ToolResult
    switch input.Type {
    case "read":
        result = tools.ReadTool{}.Run(input.Input)
    case "write":
        result = tools.WriteTool{}.Run(input.Input)
    case "edit":
        result = tools.EditTool{}.Run(input.Input)
    case "bash":
        result = tools.BashTool{}.Run(input.Input)
    default:
        result = tools.ToolResult{
            Type: "result", Success: false, 
            Error: "unknown tool: " + input.Type}
    }

    json.NewEncoder(os.Stdout).Encode(result)
}
```

- [ ] **Step 2: Commit**

```bash
git add main.go && git commit -m "feat: integrate tools in main.go"
```

---

## Task 7: Build and test

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Build**

```bash
cd /home/decodo/work/tyci-agent && go build -o tyci-agent .
```

- [ ] **Step 2: Test read**

```bash
echo '{"type":"read","input":{"path":"main.go"}}' | ./tyci-agent
# Expected: {"type":"result","success":true,"content":"..."}
```

- [ ] **Step 3: Test write**

```bash
echo '{"type":"write","input":{"path":"/tmp/test.txt","content":"hello"}}' | ./tyci-agent
# Expected: {"type":"result","success":true}
```

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "feat: tools implementation complete"
```

---

## Verification

```bash
echo '{"type":"read","input":{"path":"main.go"}}' | ./tyci-agent | jq .
echo '{"type":"write","input":{"path":"/tmp/test.txt","content":"test"}}' | ./tyci-agent | jq .
echo '{"type":"bash","input":{"command":"echo hi"}}' | ./tyci-agent | jq .
```