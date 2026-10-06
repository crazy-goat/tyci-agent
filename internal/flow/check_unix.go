//go:build !windows

package flow

import "syscall"

func procAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// signalGroup signals the whole process group led by pid.
func signalGroup(pid int, sig syscall.Signal) { _ = syscall.Kill(-pid, sig) }
