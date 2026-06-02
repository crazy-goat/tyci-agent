package tools

import (
	"bytes"
	"context"
	"os/exec"
)

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Run(ctx context.Context, input map[string]any) ToolResult {
	cmd, ok := input["command"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "command required"}
	}

	if cmd == "" {
		return ToolResult{Type: "result", Success: false, Error: "empty command"}
	}

	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	err := c.Run()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: out.String()}
	}
	return ToolResult{Type: "result", Success: true, Content: out.String()}
}
