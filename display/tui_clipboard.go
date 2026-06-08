package display

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	osc52 "github.com/aymanbagabas/go-osc52/v2"
)

var copyToClipboard = copyToClipboardImpl

func copyToClipboardImpl(text string) error {
	if text == "" {
		return fmt.Errorf("nothing to copy")
	}

	if err := copyToClipboardCommand(text); err == nil {
		return nil
	} else if osc52Enabled() {
		if oscErr := copyToClipboardOSC52(text); oscErr == nil {
			return nil
		} else {
			return fmt.Errorf("clipboard copy failed: %v; osc52 failed: %w", err, oscErr)
		}
	} else {
		return err
	}
}

func copyToClipboardCommand(text string) error {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"pbcopy"}}
	case "linux":
		candidates = [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	default:
		return fmt.Errorf("clipboard commands unsupported on %s", runtime.GOOS)
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
		return fmt.Errorf("clipboard command failed: %w", lastErr)
	}
	return fmt.Errorf("no clipboard command found")
}

func copyToClipboardOSC52(text string) error {
	seq := osc52.New(text).Limit(100000)
	if inTmux() {
		seq = seq.Tmux()
	} else if inScreen() {
		seq = seq.Screen()
	}
	s := seq.String()
	if s == "" {
		return fmt.Errorf("text too large for osc52")
	}
	_, err := os.Stderr.WriteString(s)
	return err
}

func osc52Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TYCI_OSC52")))
	if v == "0" || v == "false" || v == "off" || v == "no" {
		return false
	}
	return true
}

func inTmux() bool {
	return os.Getenv("TMUX") != ""
}

func inScreen() bool {
	term := strings.ToLower(os.Getenv("TERM"))
	return strings.HasPrefix(term, "screen")
}
