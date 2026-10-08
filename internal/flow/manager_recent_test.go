package flow

import "testing"

// RecentIn must not detect the repo again. The TUI Runs tab polls it, and
// detection runs git subprocesses.
func TestRecentInDoesNotCallInfo(t *testing.T) {
	calls := 0
	info := RepoInfo{Home: t.TempDir(), Repo: "o/r"}
	m := &Manager{Info: func() (RepoInfo, error) {
		calls++
		return info, nil
	}}
	if got := m.RecentIn(info, 5); len(got) != 0 {
		t.Fatalf("RecentIn returned %d runs, want 0", len(got))
	}
	if calls != 0 {
		t.Fatalf("RecentIn called Info %d times, want 0", calls)
	}
	m.Recent(5)
	if calls != 1 {
		t.Fatalf("Recent called Info %d times, want 1", calls)
	}
}
