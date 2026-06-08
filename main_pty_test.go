//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// ---------------------------------------------------------------------------
// Integration tests for interactive mode (using pseudo-terminal)
// ---------------------------------------------------------------------------

// openPTY creates a pseudo-terminal master/slave pair on Linux.
// The slave is set to raw mode (ISIG disabled) immediately so that
// Ctrl+C bytes are not converted to SIGINT before the child process
// can set its own terminal mode.
func openPTY(t *testing.T) (master *os.File, slave *os.File) {
	t.Helper()

	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("cannot open /dev/ptmx: %v (skipping PTY test)", err)
	}
	t.Cleanup(func() { master.Close() })

	// Get slave number
	var pts uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&pts)))
	if errno != 0 {
		t.Fatalf("TIOCGPTN: %v", errno)
	}
	slaveName := fmt.Sprintf("/dev/pts/%d", pts)

	// Unlock slave
	var unlock int32
	_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock)))
	if errno != 0 {
		t.Fatalf("TIOCSPTLCK: %v", errno)
	}

	slave, err = os.OpenFile(slaveName, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open slave %s: %v", slaveName, err)
	}
	t.Cleanup(func() { slave.Close() })

	// Set slave to raw mode so that ISIG is disabled from the start.
	// This prevents Ctrl+C (0x03) from generating SIGINT before the
	// child process sets its own terminal mode.
	rawTermios(slave.Fd())

	return master, slave
}

// rawTermios sets the terminal to raw mode (cbreak, ISIG disabled) on fd.
func rawTermios(fd uintptr) {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&t))); errno != 0 {
		return
	}
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0
	syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&t)))
}

// runInteractivePTY starts the binary in interactive mode with a PTY.
// Returns the master end for sending input.
// It waits until the prompt (">>>") appears before returning, so the
// child process is ready to accept input.
func runInteractivePTY(t *testing.T) (*os.File, *exec.Cmd) {
	t.Helper()

	master, slave := openPTY(t)

	cmd := exec.Command(binPath, "--mode", "interactive", "--model", "opencode-zen/big-pickle", "--no-session", "--history-file", "/dev/null")
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0, // fd 0 is slave, make it controlling terminal
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start interactive: %v", err)
	}

	// Wait for the prompt to appear (up to 15 seconds).
	// The first run can be slow due to terminal queries by imported libraries.
	buf := make([]byte, 4096)
	var acc strings.Builder
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		master.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := master.Read(buf)
		if err != nil {
			continue
		}
		s := string(buf[:n])
		acc.WriteString(s)
		if strings.Contains(acc.String(), ">>>") {
			break
		}
	}

	return master, cmd
}

// waitExit waits for the command to exit within the given timeout.
func waitExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) error {
	t.Helper()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		cmd.Process.Kill()
		return fmt.Errorf("timed out after %v", timeout)
	}
}

func TestInteractiveCtrlCEmptyPromptExits(t *testing.T) {
	// Ctrl+C on empty prompt should exit (like EOF)
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	master.Write([]byte{0x03})

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInteractiveCtrlCWithTextClearsThenSecondExits(t *testing.T) {
	// Ctrl+C with text in buffer should clear it; second Ctrl+C on empty buffer exits
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	// Type something
	master.Write([]byte("hello"))
	master.Write([]byte{0x03}) // Ctrl+C clears buffer
	time.Sleep(200 * time.Millisecond)

	// Second Ctrl+C on empty buffer should exit
	master.Write([]byte{0x03})

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInteractiveEOFExits(t *testing.T) {
	// Ctrl+D on empty line should exit
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	master.Write([]byte{0x04}) // Ctrl+D

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInteractiveEmptyEnterStaysThenCtrlCExits(t *testing.T) {
	// Enter on empty line should stay in prompt; Ctrl+C then exits
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	master.Write([]byte("\n")) // empty Enter
	time.Sleep(200 * time.Millisecond)

	// Should still be running; send Ctrl+C to exit
	master.Write([]byte{0x03})

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit after Ctrl+C, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInteractiveSlashNewThenSlashExit(t *testing.T) {
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	master.Write([]byte("/new\n"))
	time.Sleep(100 * time.Millisecond)
	master.Write([]byte("/exit\n"))

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInteractiveWriteAfterCtrlC(t *testing.T) {
	// After Ctrl+C clears buffer, should be able to type /exit and quit
	master, cmd := runInteractivePTY(t)
	defer master.Close()

	master.Write([]byte("some text"))
	master.Write([]byte{0x03}) // clear
	time.Sleep(100 * time.Millisecond)
	master.Write([]byte("/exit\n"))

	if err := waitExit(t, cmd, 3*time.Second); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("expected clean exit, got exit code %d", exitErr.ExitCode())
		}
		t.Fatalf("unexpected error: %v", err)
	}
}
