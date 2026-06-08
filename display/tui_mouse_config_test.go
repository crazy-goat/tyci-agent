package display

import "testing"

func TestTuiMouseEnabled_Default(t *testing.T) {
	t.Setenv("TYCI_TUI_MOUSE", "")
	if !tuiMouseEnabled() {
		t.Fatal("mouse should be enabled by default")
	}
}

func TestTuiMouseEnabled_DisabledValues(t *testing.T) {
	for _, v := range []string{"0", "false", "off", "no", " FALSE "} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TYCI_TUI_MOUSE", v)
			if tuiMouseEnabled() {
				t.Fatalf("mouse should be disabled for %q", v)
			}
		})
	}
}

func TestTuiMouseEnabled_EnabledValues(t *testing.T) {
	for _, v := range []string{"1", "true", "on", "yes", "anything"} {
		t.Run(v, func(t *testing.T) {
			t.Setenv("TYCI_TUI_MOUSE", v)
			if !tuiMouseEnabled() {
				t.Fatalf("mouse should be enabled for %q", v)
			}
		})
	}
}
