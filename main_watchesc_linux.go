//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

func applyTerminalTweaks(fd int) error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCGETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
		return errno
	}
	t.Lflag |= syscall.ISIG
	t.Oflag |= syscall.OPOST
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1 // 100ms
	if _, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, uintptr(fd), syscall.TCSETS, uintptr(unsafe.Pointer(&t)), 0, 0, 0); errno != 0 {
		return errno
	}
	return nil
}
