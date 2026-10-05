package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// withVersion sets the link-time version variable for the duration of a test.
func withVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

func TestResolveVersion_StampedWins(t *testing.T) {
	withVersion(t, "v9.9.9")

	if got := resolveVersion(); got != "v9.9.9" {
		t.Fatalf("resolveVersion() = %q, want the stamped version %q", got, "v9.9.9")
	}
}

func TestResolveVersion_ModuleVersionFallback(t *testing.T) {
	withVersion(t, "")
	oldRead := readBuildInfo
	t.Cleanup(func() { readBuildInfo = oldRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true
	}

	if got := resolveVersion(); got != "v1.2.3" {
		t.Fatalf("resolveVersion() = %q, want the module version %q", got, "v1.2.3")
	}
}

func TestResolveVersion_DevelModuleFallsBackToDev(t *testing.T) {
	withVersion(t, "")
	oldRead := readBuildInfo
	t.Cleanup(func() { readBuildInfo = oldRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}

	if got := resolveVersion(); got != "dev" {
		t.Fatalf("resolveVersion() = %q, want %q for a (devel) build", got, "dev")
	}
}

func TestResolveVersion_NoBuildInfoFallsBackToDev(t *testing.T) {
	withVersion(t, "")
	oldRead := readBuildInfo
	t.Cleanup(func() { readBuildInfo = oldRead })
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }

	if got := resolveVersion(); got != "dev" {
		t.Fatalf("resolveVersion() = %q, want %q without build info", got, "dev")
	}
}

// TestVersionFlag checks that the real root command answers `--version`
// without any provider or model configuration: cobra handles the flag before
// RunE, so nothing in initCommon runs.
func TestVersionFlag(t *testing.T) {
	oldVersion := rootCmd.Version
	oldOut := rootCmd.OutOrStdout()
	oldErr := rootCmd.ErrOrStderr()
	rootCmd.Version = "v1.2.3-test"
	rootCmd.SetArgs([]string{"--version"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	t.Cleanup(func() {
		rootCmd.Version = oldVersion
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(oldOut)
		rootCmd.SetErr(oldErr)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rootCmd.Execute(--version): %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "tyci version v1.2.3-test") {
		t.Fatalf("--version output = %q, want it to contain %q", got, "tyci version v1.2.3-test")
	}
}

// TestVersionFlagStampedBinary proves the `-ldflags "-X main.version=..."`
// path actually reaches the printed version. The linker silently ignores -X
// for a symbol that does not exist, so only an end-to-end check catches a
// renamed or moved version variable.
func TestVersionFlagStampedBinary(t *testing.T) {
	bin := buildTyciBinaryArgs(t, "-ldflags", "-X main.version=v1.2.3-test")

	out := runTyciBinary(t, bin, "--version")
	if !strings.Contains(out, "tyci version v1.2.3-test") {
		t.Fatalf("stamped binary --version output = %q, want it to contain %q", out, "tyci version v1.2.3-test")
	}
}

// TestMakefileVersionIsNotShellInterpolated guards the Makefile's version
// plumbing against shell injection. `git describe` output is used as the
// version, and Git permits backticks, `$()` and quotes in a tag. If the
// Makefile substitutes that value into the recipe text, the shell parses it as
// source and runs command substitution; it must instead reach the compiler as
// an environment value the shell only expands.
//
// The test builds the real Makefile in a throwaway Git repository tagged with
// a hostile name and runs `make release` against a fake `go` that records its
// arguments. A fixed Makefile passes the tag through literally and creates no
// marker file; the vulnerable version executes the tag and fails.
func TestMakefileVersionIsNotShellInterpolated(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	// A fake `go` that records its arguments instead of building anything.
	argsFile := filepath.Join(dir, "go-args")
	fakeGo := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$GO_ARGS_FILE\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	// The Makefile under test, in a Git repository tagged with a name that
	// would execute `touch <marker>` if it were interpolated into shell source.
	makefile, err := os.ReadFile(filepath.Join(repoDir, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}

	marker := filepath.Join(dir, "injected")
	tag := "v1.2.3-`touch${IFS}" + marker + "`"

	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "Makefile")
	git("commit", "-q", "-m", "test")
	git("tag", tag)

	cmd := exec.Command("make", "release")
	cmd.Dir = dir
	cmd.Env = setEnv(os.Environ(), "PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = setEnv(cmd.Env, "GO_ARGS_FILE", argsFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make release: %v\n%s", err, out)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("tag %q was executed as shell source: %s exists", tag, marker)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded go args: %v", err)
	}
	want := "-X main.version=" + tag
	for _, arg := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.Contains(arg, want) {
			return
		}
	}
	t.Fatalf("go was not passed %q; recorded args:\n%s", want, raw)
}

// setEnv replaces key's value in env (or appends it when absent). Appending a
// duplicate would be unreliable: a child reading the environment through libc
// getenv sees the first match, which would be the original value.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
