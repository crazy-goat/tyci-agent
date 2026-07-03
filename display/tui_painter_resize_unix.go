//go:build !windows

package display

import (
	"os"
	"os/signal"
	"syscall"
)

// watchResize invokes cb on every terminal resize (SIGWINCH) until the returned
// stop func is called. Used by the custom painter, since bubbletea installs no
// resize handling when its renderer is disabled.
func watchResize(cb func()) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	go func() {
		for range sig {
			cb()
		}
	}()
	return func() {
		signal.Stop(sig)
		close(sig)
	}
}
