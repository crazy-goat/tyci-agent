package readline

import (
	"fmt"

	"golang.org/x/term"
)

func (e *LineEditor) enableRawMode() error {
	state, err := term.MakeRaw(e.fd)
	if err != nil {
		return err
	}
	e.rawState = state
	return nil
}

func (e *LineEditor) disableRawMode() error {
	if e.rawState != nil {
		if err := term.Restore(e.fd, e.rawState); err != nil {
			return fmt.Errorf("restore terminal: %w", err)
		}
		e.rawState = nil
	}
	return nil
}
