package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/decodo/tyci-agent/stream"
)

// exitCodeHint returns a human-readable description for common exit codes.
// Returns empty string for 0 or unknown codes.
func exitCodeHint(code int) string {
	switch code {
	case 1:
		return "general error"
	case 2:
		return "misuse of shell builtins"
	case 126:
		return "command cannot execute (permission/not executable)"
	case 127:
		return "command not found"
	case 128:
		return "invalid exit argument"
	case 130:
		return "script terminated by Ctrl+C (SIGINT)"
	case 137:
		return "process killed (SIGKILL, likely out-of-memory)"
	case 139:
		return "segmentation fault (SIGSEGV)"
	case 143:
		return "process terminated (SIGTERM)"
	case 255:
		return "exit status out of range"
	default:
		if code >= 129 && code <= 159 {
			sig := code - 128
			return fmt.Sprintf("killed by signal %d", sig)
		}
		return ""
	}
}

// formatExitError formats a non-zero exit error with code and hint.
func formatExitError(err error, output string) string {
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	hint := exitCodeHint(exitCode)
	if hint != "" {
		return fmt.Sprintf("❌ exit code %d (%s):\n%s", exitCode, hint, output)
	}
	return fmt.Sprintf("❌ exit code %d:\n%s", exitCode, output)
}

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) Run(ctx context.Context, input map[string]any) ToolResult {
	cmdVal, ok := input["command"].(string)
	if !ok {
		return ToolResult{Type: "result", Success: false, Error: "command required"}
	}
	if cmdVal == "" {
		return ToolResult{Type: "result", Success: false, Error: "empty command"}
	}

	// Use pipes for both stdout and stderr
	c := exec.Command("bash", "-c", cmdVal)

	stdin, _ := c.StdinPipe()
	if stdin != nil {
		stdin.Close()
	}

	// If streaming callback is set, use pipes and stream
	if stream.OnOutput != nil {
		return t.runStreaming(ctx, c)
	}

	// Otherwise use buffers
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
			return ToolResult{Type: "result", Success: false, Error: formatExitError(err, out.String())}
		}
		return ToolResult{Type: "result", Success: true, Content: out.String()}

	case <-ctx.Done():
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		if ctx.Err() == context.DeadlineExceeded {
			return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
		}
		return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
	}
}

func (t *BashTool) runStreaming(ctx context.Context, c *exec.Cmd) ToolResult {
	toolIdx := 0
	if idx, ok := ctx.Value(stream.ToolIdxCtxKey{}).(int); ok {
		toolIdx = idx
	}

	stdoutPipe, err := c.StdoutPipe()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("stdout pipe: %v", err)}
	}
	stderrPipe, err := c.StderrPipe()
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("stderr pipe: %v", err)}
	}

	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := c.Start(); err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}

	var fullOutput strings.Builder

	// Read lines from both pipes
	lineCh := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- c.Wait()
	}()

	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				// stdout pipe closed, wait for process
				err := <-waitCh
				if err != nil {
					if ctx.Err() == context.DeadlineExceeded {
						return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
					}
					if ctx.Err() == context.Canceled {
						return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
					}
					return ToolResult{Type: "result", Success: false, Error: formatExitError(err, fullOutput.String())}
				}
				return ToolResult{Type: "result", Success: true, Content: strings.TrimRight(fullOutput.String(), "\n")}
			}
			fullOutput.WriteString(line)
			fullOutput.WriteString("\n")
			if stream.OnOutput != nil {
				stream.OnOutput(toolIdx, line)
			}

		case <-ctx.Done():
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			<-waitCh
			if ctx.Err() == context.DeadlineExceeded {
				return ToolResult{Type: "result", Success: false, Error: "bash tool timed out"}
			}
			return ToolResult{Type: "result", Success: false, Error: "bash tool cancelled"}
		}
	}
}