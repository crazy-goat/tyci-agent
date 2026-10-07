package flow

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// artifactCap is the size limit of one artifact file. A longer file keeps its
// last artifactCap bytes after a truncation line.
const artifactCap = 64 << 10

// Every step of a run gets its own artifact dir <runDir>/artifacts/NNN-<state>,
// NNN being the step seq. The runner creates it before the step and seals it
// after the step; nothing writes to it later.

// startArtifact removes the dirs of an aborted step with the same seq and
// creates the artifact dir (0700). It returns the dir name and path, both
// empty when runDir is empty.
func startArtifact(runDir string, seq int, state string) (name, dir string, err error) {
	if runDir == "" {
		return "", "", nil
	}
	base := filepath.Join(runDir, "artifacts")
	old, _ := filepath.Glob(filepath.Join(base, fmt.Sprintf("%03d-*", seq)))
	for _, p := range old {
		if err := os.RemoveAll(p); err != nil {
			return "", "", err
		}
	}
	name = fmt.Sprintf("%03d-%s", seq, state)
	dir = filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	return name, dir, nil
}

// sealArtifact masks secrets in every regular file of dir, cuts it to
// artifactCap (keeping the tail) and sets mode 0600. Best effort.
func sealArtifact(dir string) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := readTail(p, 4*artifactCap)
		if err != nil {
			continue
		}
		_ = os.WriteFile(p, []byte(capArtifact(MaskSecrets(string(data)))), 0o600)
		_ = os.Chmod(p, 0o600)
	}
}

// readTail returns the last n bytes of a file (all of it when it is shorter).
func readTail(p string, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > n {
		if _, err := f.Seek(-n, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// capArtifact keeps the last artifactCap bytes of s after a truncation line.
func capArtifact(s string) string {
	if len(s) <= artifactCap {
		return s
	}
	return fmt.Sprintf("[tyci: truncated, kept the last %d bytes]\n", artifactCap) + s[len(s)-artifactCap:]
}
