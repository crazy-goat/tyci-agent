//go:build linux

package readline

import "syscall"

// hasMoreData checks if there's more data available on stdin (non-blocking poll).
func (e *LineEditor) hasMoreData() bool {
	var rfds syscall.FdSet
	rfds.Bits[e.fd/64] |= 1 << (uint(e.fd) % 64)
	tv := syscall.NsecToTimeval(0) // no wait
	n, err := syscall.Select(e.fd+1, &rfds, nil, nil, &tv)
	return n > 0 && err == nil
}
