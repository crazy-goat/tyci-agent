package flow

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"
)

func runScript(t *testing.T, name, body string, s State, env []string, timeout time.Duration) (string, CheckResult) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &ExecChecker{DefaultTimeout: timeout, Resolve: func(string) (string, error) { return p, nil }}
	key, res, err := c.Run(context.Background(), s, env, dir)
	if err != nil {
		t.Fatal(err)
	}
	return key, res
}

func sh(t *testing.T, body string) (string, CheckResult) {
	return runScript(t, "c.sh", body, State{Check: "c.sh"}, []string{"PATH=" + os.Getenv("PATH")}, time.Minute)
}

func TestExecChecker_PrintedKey(t *testing.T) {
	if k, _ := sh(t, "echo green"); k != "green" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_LastNonEmptyLine(t *testing.T) {
	if k, _ := sh(t, "echo a; echo b; echo"); k != "b" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_ExitCodeKey(t *testing.T) {
	k, res := sh(t, "exit 3")
	if k != "3" || res.Exit == nil || *res.Exit != 3 {
		t.Fatalf("key %q exit %v", k, res.Exit)
	}
}

func TestExecChecker_NonZeroExitIgnoresPrintedKey(t *testing.T) {
	if k, _ := sh(t, "echo green; exit 1"); k != "1" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_ZeroExitNoOutputKey(t *testing.T) {
	if k, _ := sh(t, "true"); k != "0" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_Timeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	body := "sleep 30 &\necho $! > " + pidFile + "\nwait"
	start := time.Now()
	k, _ := runScript(t, "c.sh", body, State{Check: "c.sh", TimeoutSec: 1}, []string{"PATH=" + os.Getenv("PATH")}, time.Minute)
	if k != "timeout" {
		t.Fatalf("key %q", k)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %v", d)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("child %d still alive", pid)
	}
}

func TestExecChecker_KilledKey(t *testing.T) {
	if k, _ := sh(t, "kill -9 $$"); k != "killed" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_StdinIsDevNull(t *testing.T) {
	if k, _ := sh(t, "read -t1 x || echo eof"); k != "eof" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_EnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.sh")
	_ = os.WriteFile(p, []byte(`echo "$TYCI_ISSUE $PWD"`), 0o600)
	c := &ExecChecker{Resolve: func(string) (string, error) { return p, nil }}
	k, _, err := c.Run(context.Background(), State{Check: "c.sh"}, []string{"PATH=" + os.Getenv("PATH"), "TYCI_ISSUE=42"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if !strings.HasPrefix(k, "42 ") || !strings.HasSuffix(k, filepath.Base(real)) {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_UsesOnlyGivenEnv(t *testing.T) {
	t.Setenv("SECRET_X", "s3")
	env := buildCheckEnv(&RunState{}, State{}, "")
	if k, _ := runScript(t, "c.sh", `echo "[${SECRET_X:-}]"`, State{Check: "c.sh"}, env, time.Minute); k != "[]" {
		t.Fatalf("key %q", k)
	}
	if k, _ := runScript(t, "c.sh", `echo "[${SECRET_X:-}]"`, State{Check: "c.sh"}, nil, time.Minute); k != "[]" {
		t.Fatalf("nil env leaked: %q", k)
	}
}

func TestExecChecker_GhTokenPassedThrough(t *testing.T) {
	t.Setenv("GH_TOKEN", "tok")
	t.Setenv("LANG", "C")
	env := buildCheckEnv(&RunState{}, State{}, "")
	k, _ := runScript(t, "c.sh", `echo "$GH_TOKEN $LANG"`, State{Check: "c.sh"}, env, time.Minute)
	if k != "tok C" {
		t.Fatalf("key %q", k)
	}
}

func TestExecChecker_StdoutCapped(t *testing.T) {
	_, res := sh(t, `for i in $(seq 1 20000); do echo "line-$i"; done`)
	if len(res.Stdout) > 64<<10 || !strings.HasSuffix(res.Stdout, "line-20000\n") {
		t.Fatalf("len %d", len(res.Stdout))
	}
}

func TestExecChecker_StderrTail2KiB(t *testing.T) {
	_, res := sh(t, `head -c 10000 /dev/zero | tr '\0' a >&2; printf 'END' >&2`)
	if len(res.StderrTail) != 2048 || !strings.HasSuffix(res.StderrTail, "aEND") {
		t.Fatalf("len %d", len(res.StderrTail))
	}
}

func TestResolveCheck_RejectsAbsoluteAndDotDot(t *testing.T) {
	for _, rel := range []string{"/etc/passwd", "../x.sh", "checks/../../x.sh"} {
		if _, err := ResolveCheck(rel, "", "", nil, t.TempDir()); err == nil {
			t.Errorf("%q accepted", rel)
		}
	}
}

func TestResolveCheck_Order(t *testing.T) {
	repo, home, run := t.TempDir(), t.TempDir(), t.TempDir()
	emb := fstest.MapFS{"checks/a.sh": {Data: []byte("echo e")}}
	put := func(base string) string {
		p := filepath.Join(base, ".tyci", "checks", "a.sh")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		return p
	}
	hp, rp := put(home), put(repo)
	if got, _ := ResolveCheck("checks/a.sh", repo, home, emb, run); got != rp {
		t.Fatalf("project: %q", got)
	}
	if got, _ := ResolveCheck("checks/a.sh", "", home, emb, run); got != hp {
		t.Fatalf("untrusted: %q", got)
	}
	_ = os.Remove(hp)
	got, err := ResolveCheck("checks/a.sh", "", home, emb, run)
	if err != nil || got != filepath.Join(run, "checks", "a.sh") {
		t.Fatalf("embedded: %q %v", got, err)
	}
}

func TestResolveCheck_CopiesAllEmbeddedChecks(t *testing.T) {
	run := t.TempDir()
	emb := fstest.MapFS{
		"checks/rebase.sh": {Data: []byte("bash push.sh")},
		"checks/push.sh":   {Data: []byte("true")},
	}
	if _, err := ResolveCheck("checks/rebase.sh", "", "", emb, run); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(run, "checks", "push.sh")); err != nil {
		t.Fatal(err)
	}
}

func TestLuaChecker_ReturnsKey(t *testing.T) {
	if k, _ := runScript(t, "c.lua", `return env.TYCI_X`, State{Check: "c.lua"}, []string{"TYCI_X=green"}, time.Minute); k != "green" {
		t.Fatalf("key %q", k)
	}
	k, res := runScript(t, "c.lua", `return 4`, State{Check: "c.lua"}, nil, time.Minute)
	if k != "4" || res.Exit == nil || *res.Exit != 4 {
		t.Fatalf("key %q", k)
	}
}

func TestLuaChecker_NoIoOrExecute(t *testing.T) {
	k, _ := runScript(t, "c.lua", `return tostring(io) .. tostring(os.execute)`, State{Check: "c.lua"}, nil, time.Minute)
	if k != "nilnil" {
		t.Fatalf("key %q", k)
	}
}
