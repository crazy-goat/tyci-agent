package main

import (
	"bytes"
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
