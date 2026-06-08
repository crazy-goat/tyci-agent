//go:build darwin

package readline

import "golang.org/x/sys/unix"

// hasMoreData checks if there's more data available on stdin (non-blocking poll).
func (e *LineEditor) hasMoreData() bool {
	pollFds := []unix.PollFd{{Fd: int32(e.fd), Events: unix.POLLIN}}
	_, err := unix.Poll(pollFds, 0)
	return err == nil && pollFds[0].Revents&unix.POLLIN != 0
}
