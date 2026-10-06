// Package testutil holds helpers shared by the check and flow tests.
package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// StubGH writes an executable gh script to a temp dir and puts it first on PATH.
// Every invocation appends its arguments ($*) to the returned log file. body is a
// shell fragment that runs after logging; it usually is a case "$*" statement
// answering from env vars the test sets.
func StubGH(t *testing.T, body string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "gh.log")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"$GH_LOG\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_LOG", logPath)
	return logPath
}

// RunCheck runs `bash script` with a minimal env plus env. The key is the last
// non-empty trimmed stdout line when the exit code is 0, as in the real checker.
func RunCheck(t *testing.T, script string, env map[string]string) (key string, exit int, stderr string) {
	t.Helper()
	key, exit, _, stderr = RunCheckOut(t, script, env)
	return key, exit, stderr
}

// RunCheckOut is RunCheck that also returns the full stdout.
func RunCheckOut(t *testing.T, script string, env map[string]string) (key string, exit int, stdout, stderr string) {
	t.Helper()
	return RunCheckIn(t, "", script, env)
}

// RunCheckIn is RunCheckOut with the working directory set to dir (unchanged when empty).
// Give an absolute script path when dir is set.
func RunCheckIn(t *testing.T, dir, script string, env map[string]string) (key string, exit int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command("bash", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GH_LOG=" + os.Getenv("GH_LOG")}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if exit == 0 {
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		key = strings.TrimSpace(lines[len(lines)-1])
	}
	return key, exit, out.String(), errb.String()
}
