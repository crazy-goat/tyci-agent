//go:build !windows

package cron

import (
	"fmt"
	"os"
	"syscall"
)

// TryLock attempts to take an exclusive, non-blocking advisory lock on the
// file at path (created if missing). ok=false (with err=nil) means another
// process already holds it — expected overlap between two tick dispatches,
// not a failure the caller should report as one. The caller must call
// release when done, whether or not ok, to close the file handle (release is
// a harmless no-op when ok is false).
func TryLock(path string) (release func(), ok bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return func() {}, false, fmt.Errorf("cron: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return func() {}, false, nil
		}
		return func() {}, false, fmt.Errorf("cron: lock %s: %w", path, err)
	}
	release = func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
	return release, true, nil
}
