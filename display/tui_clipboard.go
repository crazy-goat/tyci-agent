package display

import (
	"fmt"
	"os/exec"
	"runtime"
)

func copyToClipboard(text string) error {
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}

	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "linux":
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	default:
		return fmt.Errorf("clipboard unsupported on %s", runtime.GOOS)
	}

	var lastErr error
	for _, args := range candidates {
		path, err := exec.LookPath(args[0])
		if err != nil {
			lastErr = err
			continue
		}
		cmd := exec.Command(path, args[1:]...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			lastErr = err
			continue
		}
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		_, writeErr := stdin.Write([]byte(text))
		closeErr := stdin.Close()
		waitErr := cmd.Wait()
		if writeErr == nil && closeErr == nil && waitErr == nil {
			return nil
		}
		if writeErr != nil {
			lastErr = writeErr
		} else if closeErr != nil {
			lastErr = closeErr
		} else {
			lastErr = waitErr
		}
	}
	if lastErr != nil {
		return fmt.Errorf("clipboard copy failed: %w", lastErr)
	}
	return fmt.Errorf("no clipboard command found")
}
