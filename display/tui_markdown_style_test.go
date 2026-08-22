package display

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// TestInlineCodeIsNotGlamoursRed pins the reason markdownStyle exists at all.
// glamour's dark style renders `inline code` in 203, a bright red, and an
// agent's answers are mostly file and function names — so the default made a
// normal reply look like a list of errors.
func TestInlineCodeIsNotGlamoursRed(t *testing.T) {
	got := markdownStyle().Code.Color
	if got == nil {
		t.Fatal("inline code has no colour configured")
	}
	if *got != inlineCodeFg {
		t.Fatalf("inline code colour is %q, want %q", *got, inlineCodeFg)
	}
	if def := styles.DarkStyleConfig.Code.Color; def != nil && *def == *got {
		t.Fatal("the override matches the default, so nothing was actually changed")
	}
}

// TestMarkdownStyleDoesNotMutateTheSharedDefault: StyleConfig holds pointers,
// and styles.DarkStyleConfig is a package-level value shared with every other
// glamour user in the process. Recolouring it in place would leak.
func TestMarkdownStyleDoesNotMutateTheSharedDefault(t *testing.T) {
	before := styles.DarkStyleConfig.Code.Color
	beforeVal := ""
	if before != nil {
		beforeVal = *before
	}

	_ = markdownStyle()

	after := styles.DarkStyleConfig.Code.Color
	afterVal := ""
	if after != nil {
		afterVal = *after
	}
	if beforeVal != afterVal {
		t.Fatalf("the shared default was mutated: %q -> %q", beforeVal, afterVal)
	}
}

// TestInlineCodeReachesTheRenderedOutput covers the wiring rather than the
// config: the style has to be the one getRenderer actually passes to glamour.
// Forced to 256 colours because the test binary has no terminal and would
// otherwise emit no colour at all.
func TestInlineCodeReachesTheRenderedOutput(t *testing.T) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(markdownStyle()),
		glamour.WithWordWrap(80),
		glamour.WithColorProfile(1), // ANSI256
	)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.Render("read `main.go` first")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "38;5;"+inlineCodeFg) {
		t.Fatalf("inline code was not painted with %s: %q", inlineCodeFg, out)
	}
	if strings.Contains(out, "38;5;203") {
		t.Fatalf("the red is still there: %q", out)
	}
}
