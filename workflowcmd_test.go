package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The Lua workflow commands are removed (#188). A script that still calls
// them must fail with exit 1, not succeed silently.
func TestRemovedWorkflowCommandsFail(t *testing.T) {
	for _, sub := range []string{"run", "list"} {
		t.Run(sub, func(t *testing.T) {
			cmd := exec.Command(binPath, "workflow", sub, "foo.lua")
			cmd.Dir = t.TempDir()
			cmd.Env = testEnv()
			out, err := cmd.CombinedOutput()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("want exit 1 for removed `workflow %s`, got err=%v: %s", sub, err, out)
			}
			if want := `unknown command "` + sub + `" for "tyci workflow"`; !strings.Contains(string(out), want) {
				t.Errorf("output should contain %q, got: %s", want, out)
			}
		})
	}
}
