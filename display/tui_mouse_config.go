package display

import (
	"os"
	"strings"
)

func tuiMouseEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("TYCI_TUI_MOUSE")))
	return !(v == "0" || v == "false" || v == "off" || v == "no")
}
