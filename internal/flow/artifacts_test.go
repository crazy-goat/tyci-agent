package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func artifactDirs(t *testing.T, runDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(runDir, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// runCheckScript runs a one-state workflow whose check is a bash script and
// returns the run state and the output.log of the step.
func runCheckScript(t *testing.T, body string) (*RunState, string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "c.sh")
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	wf := &Workflow{Name: "demo", Start: "c", States: map[string]State{
		"c":   {Check: "c.sh", On: map[string]string{"default": "end"}},
		"end": {End: true},
	}}
	runDir := t.TempDir()
	r := &Runner{WF: wf, RunDir: runDir, Checks: &ExecChecker{
		DefaultTimeout: time.Minute,
		Resolve:        func(string) (string, error) { return script, nil },
	}}
	st := newRun("")
	st.Worktree = t.TempDir()
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.History[0].Artifact != "001-c" {
		t.Fatalf("artifact = %q", st.History[0].Artifact)
	}
	p := filepath.Join(runDir, "artifacts", "001-c", "output.log")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(p)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", di.Mode().Perm())
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return st, string(b)
}

func TestArtifacts_CheckSavesFullStderr(t *testing.T) {
	st, out := runCheckScript(t, `head -c 10240 /dev/zero | tr '\0' a >&2; echo END >&2`)
	if out != strings.Repeat("a", 10240)+"END\n" {
		t.Fatalf("output.log len %d", len(out))
	}
	if len(st.History[0].StderrTail) != 2048 {
		t.Fatalf("stderr_tail len %d", len(st.History[0].StderrTail))
	}
}

func TestArtifacts_CheckOutputMaskedAndCapped(t *testing.T) {
	_, out := runCheckScript(t, `echo token ghp_abcdef123; head -c 204800 /dev/zero | tr '\0' 'x'; echo; echo "late ghp_zzz999"; echo ok`)
	if strings.Contains(out, "ghp_") {
		t.Fatal("secret not masked")
	}
	line, rest, _ := strings.Cut(out, "\n")
	if !strings.Contains(line, "truncated") || len(rest) != artifactCap {
		t.Fatalf("first line %q, rest %d bytes", line, len(rest))
	}
	if !strings.HasSuffix(rest, "late ***\nok\n") {
		t.Fatalf("tail %q", rest[len(rest)-20:])
	}
}

func TestArtifacts_ScriptFileMaskedAndCapped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.log")
	if err := os.WriteFile(p, []byte("Bearer abc\n"+strings.Repeat("y", 200<<10)), 0o644); err != nil {
		t.Fatal(err)
	}
	sealArtifact(dir)
	b, _ := os.ReadFile(p)
	line, rest, _ := strings.Cut(string(b), "\n")
	if !strings.Contains(line, "truncated") || len(rest) != artifactCap {
		t.Fatalf("first line %q, rest %d bytes", line, len(rest))
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}

func TestArtifacts_CheckGetsArtifactDirEnv(t *testing.T) {
	_, out := runCheckScript(t, `echo "dir=$TYCI_ARTIFACT_DIR"; echo ok`)
	if !strings.Contains(out, string(filepath.Separator)+filepath.Join("artifacts", "001-c")+"\n") {
		t.Fatalf("output.log %q", out)
	}
}

func TestArtifacts_OneDirPerStepAndResumeContinues(t *testing.T) {
	wf := &Workflow{Name: "demo", Start: "a", States: map[string]State{
		"a":   {Check: "a.sh", On: map[string]string{"ok": "w"}},
		"w":   {Agent: "worker", On: map[string]string{"done": "ask"}},
		"ask": {Ask: "go on?", On: map[string]string{"yes": "b"}},
		"b":   {Check: "b.sh", On: map[string]string{"ok": "end"}},
		"end": {End: true},
	}}
	runDir := t.TempDir()
	// A leftover of an aborted step 004 is cleared when step 004 starts.
	if err := os.MkdirAll(filepath.Join(runDir, "artifacts", "004-x"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := &Runner{WF: wf, RunDir: runDir,
		Checks: &fakeChecks{keys: map[string][]string{"a.sh": {"ok"}, "b.sh": {"ok"}}},
		Agents: &fakeAgents{keys: map[string][]string{"w": {"done"}}},
	}
	st := newRun("")
	if err := r.Run(context.Background(), st); !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v", err)
	}
	if err := r.Resume(context.Background(), st, "yes"); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(artifactDirs(t, runDir), " ")
	if got != "001-a 002-w 004-b" {
		t.Fatalf("dirs = %q", got)
	}
	want := []string{"001-a", "002-w", "", "004-b"}
	for i, h := range st.History {
		if h.Seq != i+1 || h.Artifact != want[i] {
			t.Errorf("step %d: seq %d artifact %q, want %q", i, h.Seq, h.Artifact, want[i])
		}
	}
}
