package flow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListingsShowsSourceParamsAndErrors(t *testing.T) {
	home, proj := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "proj")
	writeWF(t, home, "global", "home workflow")
	writeWF(t, proj, "local", "project workflow")
	// A directory without workflow.json is listed with its error.
	if err := os.MkdirAll(filepath.Join(home, ".tyci", "workflows", "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Listings(RepoInfo{Home: home, Root: proj, Trusted: true})
	if len(got) != 3 {
		t.Fatalf("got %d listings: %+v", len(got), got)
	}
	// Sorted by name: broken, global, local.
	if got[0].Name != "broken" || got[0].Err == nil || got[0].Source != "" {
		t.Errorf("broken: %+v", got[0])
	}
	if got[1].Name != "global" || got[1].Err != nil || got[1].Source != "home" {
		t.Errorf("global: %+v", got[1])
	}
	if got[2].Name != "local" || got[2].Err != nil || got[2].Source != "project" {
		t.Errorf("local: %+v", got[2])
	}
}

func TestListingsSkipsUntrustedProject(t *testing.T) {
	home, proj := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "proj")
	writeWF(t, proj, "local", "project workflow")
	if got := Listings(RepoInfo{Home: home, Root: proj, Trusted: false}); len(got) != 0 {
		t.Fatalf("untrusted project workflow listed: %+v", got)
	}
}
