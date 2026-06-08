//go:build darwin

package main

import "golang.org/x/sys/unix"

func applyTerminalTweaks(fd int) error {
	t, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	t.Lflag |= unix.ISIG
	t.Oflag |= unix.OPOST
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = 1 // 100ms
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, t)
}
