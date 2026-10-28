package flow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatParsesQualifiedIssueRef(t *testing.T) {
	open := []string{"acme/demo", "g/sub/p"}
	for in, want := range map[string]struct {
		repo  string
		issue int
	}{
		"acme/demo#12": {"acme/demo", 12},
		"g/sub/p#3":    {"g/sub/p", 3},
		" g/sub/p#3 ":  {"g/sub/p", 3},
	} {
		repo, issue, err := ParseIssueRef(in, open)
		if err != nil || repo != want.repo || issue != want.issue {
			t.Errorf("ParseIssueRef(%q) = %q, %d, %v", in, repo, issue, err)
		}
	}
	for _, in := range []string{"other/demo#1", "acme/demo#x", "acme/demo#0"} {
		if _, _, err := ParseIssueRef(in, open); err == nil {
			t.Errorf("ParseIssueRef(%q): no error", in)
		}
	}
}

func TestBareIssueRefAmbiguousWithTwoRepos(t *testing.T) {
	_, _, err := ParseIssueRef("#12", []string{"acme/demo", "other/demo"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "acme/demo#12") {
		t.Fatalf("err = %v, want an ambiguous ref that shows owner/name#N", err)
	}
	for _, in := range []string{"#12", "12"} {
		repo, issue, err := ParseIssueRef(in, []string{"acme/demo"})
		if err != nil || repo != "acme/demo" || issue != 12 {
			t.Fatalf("one open repo: ParseIssueRef(%q) = %q, %d, %v", in, repo, issue, err)
		}
	}
}

func TestRunRecordRepoRoundTrip(t *testing.T) {
	home := t.TempDir()
	dir := RunDir(home, RepoKey("g/sub/p"), "1")
	st := &RunState{Version: 1, Run: "1", Workflow: "demo", Repo: "g/sub/p", Status: "done", Current: "end",
		Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: dir}).Save(st); err != nil {
		t.Fatal(err)
	}
	got, ok, err := RunByID(home, RepoKey("g/sub/p"), "#1")
	if err != nil || !ok || got.Repo != "g/sub/p" {
		t.Fatalf("RunByID = %+v, %v, %v", got, ok, err)
	}
}

func TestStartRefusedOverWorkerLimit(t *testing.T) {
	c := &gatedChecks{release: make(chan struct{}), key: "ok"}
	e := newMgrEnv(t, c)
	e.m.SetWorkers(1)
	if _, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"1"}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"2"}})
	if !errors.Is(err, ErrNoSlot) || !strings.Contains(err.Error(), "orchestrator.workers is 1") || !strings.Contains(err.Error(), "workflow_stop") {
		t.Fatalf("err = %v, want the worker limit refusal", err)
	}
	close(c.release)
	e.notice(t)
	waitIdle(t, e.m)
}

// TestTwoReposShareWorkers runs two repositories in one session, each with
// its own Manager and run dir, against one worker limit.
func TestTwoReposShareWorkers(t *testing.T) {
	ca := &gatedChecks{release: make(chan struct{}), key: "ok"}
	cb := &gatedChecks{release: make(chan struct{}), key: "ok"}
	a, b := newMgrEnv(t, ca), newMgrEnv(t, cb)
	infoB := RepoInfo{Home: b.home, Root: b.home, Repo: "x/r", DefaultBranch: "main"}
	b.m.Info = func() (RepoInfo, error) { return infoB, nil }
	a.m.SetWorkers(2)
	rs := &Repos{
		Primary:  a.m,
		Registry: func() ([]string, error) { return []string{"x/r"}, nil },
		New: func(repo string) (*Manager, error) {
			if repo != "x/r" {
				t.Fatalf("New(%q)", repo)
			}
			return b.m, nil
		},
	}
	ctx := context.Background()
	if _, _, err := rs.Start(ctx, "", "demo", []string{"#1"}); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("bare #1 with two repos: err = %v", err)
	}
	if _, _, err := rs.Start(ctx, "", "demo", []string{"docs/README.md#usage"}); err == nil || strings.Contains(err.Error(), "not open") {
		t.Fatalf("a param with # that is not an issue ref: err = %v, want the ambiguous repo, not a repo lookup", err)
	}
	idA, _, err := rs.Start(ctx, "", "demo", []string{"o/r#1"})
	if err != nil || !strings.HasPrefix(idA, "o/r#") {
		t.Fatalf("start in o/r: %q, %v", idA, err)
	}
	idB, _, err := rs.Start(ctx, "x/r", "demo", []string{"2"})
	if err != nil || !strings.HasPrefix(idB, "x/r#") {
		t.Fatalf("start in x/r: %q, %v", idB, err)
	}
	if _, _, err := rs.Start(ctx, "", "demo", []string{"o/r#3"}); !errors.Is(err, ErrNoSlot) || !strings.Contains(err.Error(), "2 run(s) are active") {
		t.Fatalf("third run over the shared limit: err = %v", err)
	}
	// The test env saves runs under the key "r", so check the routing of a
	// qualified run id with a run record under the key of x/r.
	saved := &RunState{Version: 1, Run: "9", Workflow: "demo", Repo: "x/r", Status: "done", Current: "end",
		Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: RunDir(b.home, infoB.Key(), "9")}).Save(saved); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Status("x/r#9"); err != nil {
		t.Fatalf("Status(x/r#9): %v", err)
	}
	if _, err := rs.Status("#9"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Status(#9) with two repos: err = %v", err)
	}
	close(ca.release)
	close(cb.release)
	a.notice(t)
	b.notice(t)
	waitIdle(t, a.m)
	waitIdle(t, b.m)
}

// TestSharedLimitIsAtomicAcrossRepos starts one run in each of two repos at the
// same time, with one free worker slot. Exactly one start must pass.
func TestSharedLimitIsAtomicAcrossRepos(t *testing.T) {
	for range 20 {
		ca := &gatedChecks{release: make(chan struct{}), key: "ok"}
		cb := &gatedChecks{release: make(chan struct{}), key: "ok"}
		a, b := newMgrEnv(t, ca), newMgrEnv(t, cb)
		pool := &Pool{}
		pool.Join(a.m)
		pool.Join(b.m)
		a.m.SetWorkers(1)
		start := make(chan struct{})
		errs := make(chan error, 2)
		for _, e := range []*mgrEnv{a, b} {
			go func() {
				<-start
				_, _, err := e.m.Start(context.Background(), StartRequest{Workflow: "demo", Params: []string{"1"}})
				errs <- err
			}()
		}
		close(start)
		ok, noSlot := 0, 0
		for range 2 {
			switch err := <-errs; {
			case err == nil:
				ok++
			case errors.Is(err, ErrNoSlot):
				noSlot++
			default:
				t.Fatal(err)
			}
		}
		if ok != 1 || noSlot != 1 {
			t.Fatalf("%d starts passed and %d were refused; want 1 and 1", ok, noSlot)
		}
		close(ca.release)
		close(cb.release)
		select { // the end notice of the one started run
		case <-a.notices:
		case <-b.notices:
		case <-time.After(5 * time.Second):
			t.Fatal("no notice")
		}
		waitIdle(t, a.m)
		waitIdle(t, b.m)
	}
}

func TestOpenRepoReadsRegistryPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "git@github.com:acme/demo.git"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".tyci"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"repos":{"acme/demo":{"path":"` + repo + `"},"other/demo":{"path":"` + repo + `"},"no/path":{}}}`
	if err := os.WriteFile(filepath.Join(home, ".tyci", "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := RegistryRepos(home)
	if err != nil || strings.Join(names, ",") != "acme/demo,other/demo" {
		t.Fatalf("RegistryRepos = %v, %v", names, err)
	}
	info, err := OpenRepo(home, "acme/demo")
	if err != nil || info.Repo != "acme/demo" || info.Key() != "acme+demo" {
		t.Fatalf("OpenRepo = %+v, %v", info, err)
	}
	if _, err := OpenRepo(home, "other/demo"); err == nil || !strings.Contains(err.Error(), "origin remote is acme/demo") {
		t.Fatalf("origin mismatch: err = %v", err)
	}
	if _, err := OpenRepo(home, "no/path"); err == nil || !strings.Contains(err.Error(), "has no path") {
		t.Fatalf("no path: err = %v", err)
	}
}

// twoRepos returns a Repos with the primary repo o/r and the registry repo x/r,
// and a broken registry entry bad/r that does not open.
func twoRepos(t *testing.T) (*Repos, *mgrEnv, *mgrEnv) {
	t.Helper()
	a, b := newMgrEnv(t, &gatedChecks{key: "ok"}), newMgrEnv(t, &gatedChecks{key: "ok"})
	infoB := RepoInfo{Home: b.home, Root: b.home, Repo: "x/r", DefaultBranch: "main"}
	b.m.Info = func() (RepoInfo, error) { return infoB, nil }
	rs := &Repos{
		Primary:  a.m,
		Registry: func() ([]string, error) { return []string{"bad/r", "x/r"}, nil },
		New: func(repo string) (*Manager, error) {
			if repo != "x/r" {
				return nil, errors.New("repos." + repo + ".path: the origin remote is other/r")
			}
			return b.m, nil
		},
	}
	return rs, a, b
}

func TestBrokenRegistryEntryIsNotOpen(t *testing.T) {
	rs, _, _ := twoRepos(t)
	if got := strings.Join(rs.Open(), ","); got != "o/r,x/r" {
		t.Fatalf("Open = %s, want o/r,x/r", got)
	}
	if _, err := rs.Status("bad/r#1"); err == nil || !strings.Contains(err.Error(), "origin remote is other/r") {
		t.Fatalf("Status(bad/r#1): err = %v, want the reason", err)
	}
	only := &Repos{Primary: rs.Primary, Registry: func() ([]string, error) { return []string{"bad/r"}, nil }, New: rs.New}
	if got := strings.Join(only.Open(), ","); got != "o/r" {
		t.Fatalf("Open with only a broken entry = %s, want o/r", got)
	}
}

func TestStatusWithoutRunIsNewestOfPrimary(t *testing.T) {
	rs, a, _ := twoRepos(t)
	st := &RunState{Version: 1, Run: "3", Workflow: "demo", Repo: "o/r", Status: "done", Current: "end",
		Visits: map[string]int{}, History: []Step{}}
	if err := (&Store{Dir: RunDir(a.home, RepoKey("o/r"), "3")}).Save(st); err != nil {
		t.Fatal(err)
	}
	got, err := rs.Status("")
	if err != nil {
		t.Fatalf("Status(\"\") with two open repos: %v", err)
	}
	if b, _ := json.Marshal(got); !strings.Contains(string(b), `"#3"`) {
		t.Fatalf("Status(\"\") = %s, want run #3 of o/r", b)
	}
}

func TestReplaceBareRunFindsItsRepo(t *testing.T) {
	rs, a, b := twoRepos(t)
	setActive := func(m *Manager, run string, on bool) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.active == nil {
			m.active = map[string]activeRun{}
		}
		if on {
			m.active[run] = activeRun{slot: &agentSlot{}}
		} else {
			delete(m.active, run)
		}
	}
	// An empty reason fails only after the run is found, so the error tells
	// which Manager got the call.
	setActive(b.m, "7", true)
	if err := rs.Replace("7/worker", "worker", ""); err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("run 7 active in x/r: err = %v, want the call routed to x/r", err)
	}
	setActive(a.m, "7", true)
	if err := rs.Replace("7/worker", "worker", ""); err == nil || !strings.Contains(err.Error(), "active in 2 repositories") {
		t.Fatalf("run 7 active in both: err = %v, want ambiguous", err)
	}
	if err := rs.Replace("x/r#7/worker", "worker", ""); err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("qualified x/r#7: err = %v", err)
	}
	setActive(a.m, "7", false)
	setActive(b.m, "7", false)
	setActive(a.m, "8", true)
	if err := rs.Replace("8/worker", "worker", ""); err == nil || !strings.Contains(err.Error(), "reason is required") {
		t.Fatalf("orchestrator run 8 of o/r: err = %v, want the call routed to o/r", err)
	}
	setActive(a.m, "8", false)
}
