//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// setProcAttrs configures cmd to start in its own process group, so that
// signaling the group (see killProcessGroup) reaches wrapper scripts (e.g.
// "npx ...") and the real process they exec, not just the direct child.
func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the process group started by cmd, so a
// wrapper script's real child (e.g. the "node" a "npx" shim execs) dies too.
// Falls back to killing just the direct child if the group can't be resolved.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		cmd.Process.Kill()
		return
	}
	// Negative pid signals the whole process group.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}
