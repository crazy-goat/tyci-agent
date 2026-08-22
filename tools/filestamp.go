package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// File-freshness bookkeeping for the write tool.
//
// The problem this solves: "write" replaces whole files. Edit mode reads the
// file, substitutes oldString, and writes the result back; range mode
// addresses lines by number. Both are only correct against the bytes the
// agent actually looked at. If a human saves the file in their editor between
// the agent's read and its write — or a build step regenerates it, or a
// sibling subagent edits it — the write silently discards those changes, and
// nothing in the transcript hints that it happened. Line numbers are worse
// than the content case: they still "succeed", just against the wrong lines.
//
// So every read records what it saw, and every destructive write refuses to
// run unless the recorded state still matches what is on disk.
//
// Deliberately mtime+size and not a content hash: this runs on every read of
// every file, hashing a 256 KiB file to guard a one-line edit is not worth
// it, and the pair catches every case an editor or a build tool produces. A
// change that preserves both is possible in theory and not worth defending
// against here.
//
// Scope is the process, not the session: a resumed session starts with no
// stamps, so its first write to a pre-existing file asks for a fresh read.
// That is the safe direction to be wrong in.

// maxFileStamps bounds the map. One entry per file ever read is small, but a
// long session that greps and reads its way through a large monorepo would
// keep every path it touched for the lifetime of the process.
//
// When the cap is hit the whole map is dropped rather than evicting a least
// recently used entry: forgetting a stamp fails CLOSED (the next write to
// that file asks for a re-read), so the cheap option is also the safe one,
// and it needs no per-entry bookkeeping on the hot path.
const maxFileStamps = 5000

type fileStamp struct {
	modTime time.Time
	size    int64
}

var fileStamps = struct {
	mu sync.Mutex
	m  map[string]fileStamp
}{m: make(map[string]fileStamp)}

// stampKey normalises a path so that "./x.go", "x.go" and an absolute path
// to the same file share one entry.
//
// The directory is resolved through symlinks, the filename is not. Without
// the first part, macOS alone breaks the guard: /var is a symlink to
// /private/var, so a relative path resolved via the working directory and an
// absolute path handed in by the caller name the same file with different
// strings, and a read of one would not satisfy a write of the other. Without
// the second part, two distinct symlinks pointing at one file would collapse
// into a single entry — harmless in principle, but it would make a stamp
// recorded for one path silently authorise a write through another name.
//
// Every failure falls back to the less-resolved form. That can only cost a
// missed match, which means an extra read — never a false match, which would
// mean lost work.
func stampKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	dir, base := filepath.Split(abs)
	realDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return abs
	}
	return filepath.Join(realDir, base)
}

// recordFileStamp remembers the current on-disk state of path as "what the
// agent has seen". Called after a successful read, and after our own writes
// so that a second edit to the same file in the same turn does not have to
// re-read it.
func recordFileStamp(path string) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	fileStamps.mu.Lock()
	defer fileStamps.mu.Unlock()
	if len(fileStamps.m) >= maxFileStamps {
		fileStamps.m = make(map[string]fileStamp, 1)
	}
	fileStamps.m[stampKey(path)] = fileStamp{modTime: info.ModTime(), size: info.Size()}
}

// forgetFileStamp drops the record for path. Used when a write fails partway:
// we no longer know what is on disk, so the next write should re-read.
func forgetFileStamp(path string) {
	fileStamps.mu.Lock()
	defer fileStamps.mu.Unlock()
	delete(fileStamps.m, stampKey(path))
}

// ResetFileStamps clears every record. Exported for tests, which share one
// process and would otherwise leak freshness across cases.
func ResetFileStamps() {
	fileStamps.mu.Lock()
	defer fileStamps.mu.Unlock()
	fileStamps.m = make(map[string]fileStamp)
}

// checkFileFresh reports whether a destructive write to path is safe: either
// the file does not exist yet (nothing to lose), or its current state matches
// what the agent last read.
//
// The returned error is written for the model, not for a log: it has to say
// what to do next, because "write failed" with no instruction just produces a
// retry of the same call.
func checkFileFresh(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		// Missing file: this write creates it, so there is nothing to
		// clobber. Any other stat error will resurface from the write
		// itself with a better message than we could invent here.
		return nil
	}
	if info.IsDir() {
		return nil // the write path reports this better than we would
	}

	fileStamps.mu.Lock()
	stamp, seen := fileStamps.m[stampKey(path)]
	fileStamps.mu.Unlock()

	if !seen {
		return fmt.Errorf("refusing to modify %s: you have not read it, so this write could silently discard content you have never seen. Read it first, then write", path)
	}
	if !info.ModTime().Equal(stamp.modTime) || info.Size() != stamp.size {
		return fmt.Errorf("refusing to modify %s: it changed on disk after you read it (someone else's edit, a build step, or a parallel subagent). Read it again — your line numbers and oldString may no longer match — then redo the write", path)
	}
	return nil
}

// refreshFileStampIfKnown updates the record for path only when one already
// exists. Used by append: it keeps a known file's record current without
// granting freshness to a file the agent has never read.
func refreshFileStampIfKnown(path string) {
	fileStamps.mu.Lock()
	_, seen := fileStamps.m[stampKey(path)]
	fileStamps.mu.Unlock()
	if seen {
		recordFileStamp(path)
	}
}
