//go:build windows

package cron

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// TryLock is the Windows counterpart to lock_unix.go's syscall.Flock version:
// same contract (see that file's doc comment), implemented with
// LockFileEx's exclusive, non-blocking mode instead of flock(2).
func TryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, false, fmt.Errorf("cron: open lock file: %w", err)
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION {
			return func() {}, false, nil
		}
		return func() {}, false, fmt.Errorf("cron: lock %s: %w", path, err)
	}
	release = func() {
		windows.UnlockFileEx(h, 0, 1, 0, ol)
		f.Close()
	}
	return release, true, nil
}
