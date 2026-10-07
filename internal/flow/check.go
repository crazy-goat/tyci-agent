package flow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Check script contract (SDR 5.3):
//   - cwd is the run worktree, no args, stdin is /dev/null.
//   - The env is exactly what the runner passes (see buildCheckEnv).
//     ExecChecker never appends the parent env.
//   - Timeout is the state timeout_sec, else DefaultTimeout. On timeout the
//     whole process group gets SIGTERM, then SIGKILL after 5 s. Key: "timeout".
//     A process killed by a signal gives key "killed".
//   - Key: when the exit code is 0 and the last non-empty trimmed stdout line
//     is non-empty, that line; else the exit code as a decimal string.
//   - stdout keeps the last 64 KiB; stderr keeps the last 2 KiB.
//   - Output holds stdout and stderr as they arrive (the last 128 KiB); the
//     runner saves it as output.log in the step artifact dir (64 KiB limit).
//
// Builtin scripts start with `set -euo pipefail` (documented, not enforced).
const (
	stdoutCap = 64 << 10
	stderrCap = 2 << 10
	killGrace = 5 * time.Second
)

// ExecChecker runs check scripts. Scripts ending in .lua run in a sandboxed
// Lua state; everything else runs with bash.
type ExecChecker struct {
	DefaultTimeout time.Duration
	Resolve        func(rel string) (abs string, err error)
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// lockedTail is a tailBuffer that stdout and stderr copy goroutines share.
type lockedTail struct {
	mu sync.Mutex
	tailBuffer
}

func (l *lockedTail) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tailBuffer.Write(p)
}

func (l *lockedTail) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tailBuffer.String()
}

// Run implements CheckRunner.
func (c *ExecChecker) Run(ctx context.Context, s State, env []string, dir string) (string, CheckResult, error) {
	var res CheckResult
	if c.Resolve == nil {
		return "", res, errors.New("check: no resolver configured")
	}
	script, err := c.Resolve(s.Check)
	if err != nil {
		return "", res, err
	}
	timeout := c.DefaultTimeout
	if s.TimeoutSec > 0 {
		timeout = time.Duration(s.TimeoutSec) * time.Second
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if strings.HasSuffix(script, ".lua") {
		return runLuaCheck(ctx, script, env)
	}

	if env == nil {
		env = []string{} // nil would make exec inherit the parent env
	}
	stdout := &tailBuffer{max: stdoutCap}
	stderr := &tailBuffer{max: stderrCap}
	output := &lockedTail{tailBuffer: tailBuffer{max: 2 * artifactCap}}
	cmd := exec.CommandContext(ctx, "bash", script)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(stdout, output)
	cmd.Stderr = io.MultiWriter(stderr, output)
	cmd.SysProcAttr = procAttr()
	cmd.Cancel = func() error {
		pid := cmd.Process.Pid
		signalGroup(pid, syscall.SIGTERM)
		time.AfterFunc(killGrace, func() { signalGroup(pid, syscall.SIGKILL) })
		return nil
	}
	cmd.WaitDelay = killGrace

	runErr := cmd.Run()
	res.Stdout = stdout.String()
	res.StderrTail = stderr.String()
	res.Output = output.String()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout", res, nil
	}
	if cmd.ProcessState == nil {
		return "", res, fmt.Errorf("check %s: %w", s.Check, runErr)
	}
	code := cmd.ProcessState.ExitCode()
	if code < 0 {
		return "killed", res, nil
	}
	res.Exit = &code
	if code == 0 {
		if k := lastLine(res.Stdout); k != "" {
			return k, res, nil
		}
	}
	return strconv.Itoa(code), res, nil
}

func lastLine(out string) string {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// ResolveCheck finds a check script (SDR 5.2). Order: <repoDir>/.tyci/<rel>
// (callers pass repoDir="" when the repo is untrusted), <home>/.tyci/<rel>,
// then the embedded copy. On first use of an embedded script, all embedded
// checks/*.sh are copied into <runDir>/checks (mode 0700) so siblings exist.
func ResolveCheck(rel, repoDir, home string, embedded fs.FS, runDir string) (string, error) {
	if rel == "" || path.IsAbs(rel) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("check path %q must be relative", rel)
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("check path %q must not contain a parent segment", rel)
		}
	}
	for _, base := range []string{repoDir, home} {
		if base == "" {
			continue
		}
		p := filepath.Join(base, ".tyci", filepath.FromSlash(rel))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if embedded == nil {
		return "", fmt.Errorf("check %q not found", rel)
	}
	if _, err := fs.Stat(embedded, path.Clean(rel)); err != nil {
		return "", fmt.Errorf("check %q not found", rel)
	}
	matches, err := fs.Glob(embedded, "checks/*.sh")
	if err != nil {
		return "", err
	}
	dstDir := filepath.Join(runDir, "checks")
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return "", err
	}
	for _, m := range matches {
		data, err := fs.ReadFile(embedded, m)
		if err != nil {
			return "", err
		}
		dst := filepath.Join(dstDir, path.Base(m))
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.WriteFile(dst, bytes.Clone(data), 0o700); err != nil {
			return "", err
		}
	}
	return filepath.Join(runDir, filepath.FromSlash(path.Clean(rel))), nil
}
