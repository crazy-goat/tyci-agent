package agentdefs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// managedStateFile is the bookkeeping file Sync uses to tell "a file we
// installed and the user left alone" apart from "a file the user created or
// edited themselves". It deliberately has no .md suffix and a leading dot so
// LoadDir's `strings.HasSuffix(name, ".md")` filter skips it — verified by
// TestSync_ManagedStateFileNotSeenAsAgent — otherwise it would show up as a
// garbage entry in `agent list`.
const managedStateFile = ".managed.json"

// SyncResult reports what Sync did, per builtin definition name (the
// filename without its .md suffix — the same Name a parsed Def carries).
type SyncResult struct {
	Installed       []string // written for the first time
	Updated         []string // ours, unmodified by the user, content changed => overwritten
	SkippedModified []string // user edited it => left alone
	SkippedDeleted  []string // user deleted it => not resurrected
	Unchanged       []string // ours, already identical
}

// Changed reports whether Sync wrote anything to disk.
func (r SyncResult) Changed() bool {
	return len(r.Installed) > 0 || len(r.Updated) > 0
}

// Sync unpacks the embedded builtin agent definitions into dir, updating
// files tyci itself installed as long as the user has not touched them, and
// leaving everything else alone. dir does not need to exist yet.
//
// Decision table per builtin file (state = recorded sha256 in
// <dir>/.managed.json, disk = current file on disk), force == false:
//
//	state?    disk?    disk sha256 == state?   =>  action
//	no        no       -                       =>  write, Installed
//	yes       no       -                       =>  skip,  SkippedDeleted (user deleted it on purpose)
//	no        yes      -                       =>  skip,  SkippedModified (not ours, or state was lost)
//	yes       yes      no                      =>  skip,  SkippedModified (user edited it)
//	yes       yes      yes, content unchanged  =>  noop,  Unchanged
//	yes       yes      yes, content changed    =>  write, Updated (ours, safe to advance)
//
// force == true short-circuits all of the above: every builtin file is
// written unconditionally (Installed if missing, Updated if present and
// different, Unchanged if present and already byte-identical) and the state
// entry is (re)synced to match. This is the explicit "give me back the
// stock version" escape hatch, so it is allowed to overwrite user edits and
// resurrect user-deleted files — both SkippedModified and SkippedDeleted
// never occur when force is true.
//
// The state file itself is best-effort: if it is missing or its JSON is
// corrupt, Sync treats it as empty rather than failing the whole call. The
// safe direction for "state unknown" is to under-update, not over-update —
// an empty state makes every already-installed-but-untracked file look like
// "not ours" and fall into SkippedModified, which merely stalls updates
// instead of silently clobbering a file the user may have edited. A hard
// error here would also mean one damaged bookkeeping file breaks agent
// availability entirely, which is a worse failure mode than a missed update.
func Sync(dir string, force bool) (SyncResult, error) {
	var result SyncResult

	entries, err := builtinFS.ReadDir(builtinDirName)
	if err != nil {
		return result, fmt.Errorf("read embedded builtin dir: %w", err)
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return result, fmt.Errorf("create agents dir %s: %w", dir, err)
	}

	statePath := filepath.Join(dir, managedStateFile)
	state := loadManagedState(statePath)
	stateChanged := false

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		name := strings.TrimSuffix(filename, ".md")

		content, err := builtinFS.ReadFile(builtinDirName + "/" + filename)
		if err != nil {
			return result, fmt.Errorf("read embedded %s: %w", filename, err)
		}

		diskPath := filepath.Join(dir, filename)
		diskContent, readErr := os.ReadFile(diskPath)
		diskExists := readErr == nil

		if force {
			if diskExists && bytes.Equal(diskContent, content) {
				result.Unchanged = append(result.Unchanged, name)
				state[filename] = sha256Hex(content)
				stateChanged = true
				continue
			}
			if err := os.WriteFile(diskPath, content, 0644); err != nil {
				return result, fmt.Errorf("write %s: %w", diskPath, err)
			}
			state[filename] = sha256Hex(content)
			stateChanged = true
			if diskExists {
				result.Updated = append(result.Updated, name)
			} else {
				result.Installed = append(result.Installed, name)
			}
			continue
		}

		if !diskExists {
			if _, tracked := state[filename]; tracked {
				result.SkippedDeleted = append(result.SkippedDeleted, name)
				continue
			}
			if err := os.WriteFile(diskPath, content, 0644); err != nil {
				return result, fmt.Errorf("write %s: %w", diskPath, err)
			}
			state[filename] = sha256Hex(content)
			stateChanged = true
			result.Installed = append(result.Installed, name)
			continue
		}

		// File exists on disk: it is ours to touch only if its current
		// content still matches the sha256 we recorded the last time WE
		// wrote it. Anything else — a hand-edited file, or a file we never
		// tracked at all — is left untouched.
		recordedSum, tracked := state[filename]
		if !tracked || sha256Hex(diskContent) != recordedSum {
			result.SkippedModified = append(result.SkippedModified, name)
			continue
		}

		if bytes.Equal(diskContent, content) {
			result.Unchanged = append(result.Unchanged, name)
			continue
		}

		if err := os.WriteFile(diskPath, content, 0644); err != nil {
			return result, fmt.Errorf("write %s: %w", diskPath, err)
		}
		state[filename] = sha256Hex(content)
		stateChanged = true
		result.Updated = append(result.Updated, name)
	}

	if stateChanged {
		if err := saveManagedState(statePath, state); err != nil {
			return result, err
		}
	}

	return result, nil
}

// loadManagedState reads the sha256 bookkeeping map. A missing or corrupt
// file yields an empty map rather than an error — see Sync's doc comment
// for why that is the safe direction to fail in.
func loadManagedState(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	var state map[string]string
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]string{}
	}
	if state == nil {
		state = map[string]string{}
	}
	return state
}

// saveManagedState writes the sha256 bookkeeping map. Permissions match the
// rest of tyci's config files (see agent.SaveGlobal): 0644, world-readable,
// no secrets live here.
func saveManagedState(path string, state map[string]string) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal managed state: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write managed state %s: %w", path, err)
	}
	return nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
