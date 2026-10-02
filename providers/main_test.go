package providers

import (
	"os"
	"testing"
)

// packageDir is the providers directory TestMain started in, for tests that
// read files next to the sources (golden files).
var packageDir string

// TestMain runs the tests outside the repository with an empty HOME: the
// system prompt embeds the AGENTS.md found from the working directory upwards
// and the user's instructions from HOME, so without this the results would
// depend on the checkout and on the machine.
func TestMain(m *testing.M) {
	var err error
	packageDir, err = os.Getwd()
	if err != nil {
		os.Stderr.WriteString("getwd: " + err.Error() + "\n")
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "tyci-providers-test")
	if err != nil {
		os.Stderr.WriteString("mkdir temp: " + err.Error() + "\n")
		os.Exit(1)
	}
	if err := os.Chdir(dir); err != nil {
		os.Stderr.WriteString("chdir: " + err.Error() + "\n")
		os.Exit(1)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		os.Stderr.WriteString("setenv HOME: " + err.Error() + "\n")
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
