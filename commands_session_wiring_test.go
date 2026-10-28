package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/hooks"
	"github.com/crazy-goat/tyci-agent/providers"
	"github.com/crazy-goat/tyci-agent/tools"
)

// The session folder of the main chat is wired in initCommon (commands.go):
// stale folders of exited tyci processes are removed first, the run folder of
// the chat is set in the returned ctx, and shutdown removes the folder. Each
// check fails when its line in commands.go is deleted.
func TestInitCommon_SessionFolderWiring(t *testing.T) {
	writeTrustWiringHome(t)
	writeTrustWiringProject(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	defer hooks.SetForTesting(nil)()

	// A session folder of a tyci process that has exited: its PID is free.
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(tmp, fmt.Sprintf("tyci-session-%d-x", child.Process.Pid))
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}

	prov := &fakeProvider{name: "session-wiring-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)
	cmd := newInitCommonTestCmd(t)
	setDefaultModel(t, "session-wiring-prov/m1")
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp flag: %v", err)
	}

	_, _, _, ctx, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	if dl != nil {
		defer dl.Close()
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the stale session folder of an exited process survived start-up: %v", err)
	}
	dir := tools.RunDirFrom(ctx)
	if dir == "" {
		t.Fatal("the ctx of the chat has no run folder: tool_create would refuse")
	}
	if filepath.Dir(dir) != tmp {
		t.Fatalf("run folder %q is not a session folder in TMPDIR %q", dir, tmp)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the run folder of the chat does not exist: %v", err)
	}

	shutdown()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the session folder survived shutdown: %v", err)
	}
}
