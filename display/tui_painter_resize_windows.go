//go:build windows

package display

// watchResize is a no-op on Windows, which has no SIGWINCH. The custom painter
// is intended for Unix terminals; on Windows the default bubbletea renderer
// should be used instead.
func watchResize(cb func()) (stop func()) {
	return func() {}
}
