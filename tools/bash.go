package tools

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
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

	c := exec.Command("bash", "-c", cmd)

	// Close stdin immediately – commands waiting for input get EOF/pipe-closed error
	// instead of hanging forever.
	stdin, _ := c.StdinPipe()
	if stdin != nil {
		stdin.Close()
	}

	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := c.Start(); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	done := make(chan error, 1)
	go func() {
		done <- c.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
			}
			if ctx.Err() == context.Canceled {
				return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
			}
			return ToolResult{Type: "result", Success: false, Error: out.String()}
		}
		return ToolResult{Type: "result", Success: true, Content: out.String()}

	case <-ctx.Done():
		// Kill entire process group (negative PID = group), so children die too
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done // wait for process to actually die
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
		}
		return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
	}
}
