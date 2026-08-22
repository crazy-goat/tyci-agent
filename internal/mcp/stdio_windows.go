//go:build windows

package mcp

import "os/exec"

// setProcAttrs is a no-op on Windows: there is no POSIX process-group
// concept to opt into here. Windows job objects would be the equivalent,
// but that's out of scope for this fix.
func setProcAttrs(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing the direct child on Windows.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
