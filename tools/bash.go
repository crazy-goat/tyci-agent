package tools

import (
	"bytes"
	"os/exec"
	"strings"
)

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Run(input map[string]any) ToolResult {
	cmd, ok := input["command"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "command required"}
	}

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return ToolResult{Type: "result", Success: false, Error: "empty command"}
	}

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
