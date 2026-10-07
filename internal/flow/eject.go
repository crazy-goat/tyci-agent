package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

type ejectFile struct {
	rel  string // relative to .tyci
	data []byte
	mode os.FileMode
}

// Eject copies the builtin workflow name into <dir>/.tyci: the workflow JSON,
// all check scripts (they source each other), the task templates of its agent
// states and the prompts of its builtin roles. It sets roles.<role>.prompt to
// "@prompts/<role>.md" in <dir>/.tyci/config.json. Without force it changes
// nothing when a file or a different role prompt exists. It returns the
// written paths.
func Eject(name, dir string, force bool) ([]string, error) {
	return eject(name, dir, force, false)
}

// EjectMissing is Eject that writes only the missing files and sets only the
// role prompts that are not set yet. Existing files and role prompts are kept.
func EjectMissing(name, dir string) ([]string, error) {
	return eject(name, dir, false, true)
}

func eject(name, dir string, force, keep bool) ([]string, error) {
	if !workflowName.MatchString(name) {
		return nil, fmt.Errorf("bad workflow name %q: use a-z, 0-9 and -", name)
	}
	data, err := embedded.ReadFile("builtin/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("builtin workflow %q not found", name)
	}
	wf, err := Parse(data)
	if err != nil {
		return nil, err
	}
	files := []ejectFile{{rel: "workflows/" + name + ".json", data: data, mode: 0o644}}
	checks, err := fs.Glob(embedded, "checks/*.sh")
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		b, err := embedded.ReadFile(c)
		if err != nil {
			return nil, err
		}
		files = append(files, ejectFile{rel: c, data: b, mode: 0o755})
	}
	tasks, roles := map[string]bool{}, map[string]bool{}
	for _, s := range wf.States {
		if s.Task != "" {
			tasks[s.Task] = true
		}
		if s.Agent != "" {
			roles[s.Agent] = true
		}
	}
	for _, t := range sortedSet(tasks) {
		b, err := embedded.ReadFile("tasks/" + t + ".md")
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", t, err)
		}
		files = append(files, ejectFile{rel: "tasks/" + t + ".md", data: b, mode: 0o644})
	}
	var prompted []string
	for _, r := range sortedSet(roles) {
		if p, ok := flowconfig.DefaultPrompt(r); ok {
			files = append(files, ejectFile{rel: "prompts/" + r + ".md", data: []byte(p), mode: 0o644})
			prompted = append(prompted, r)
		}
	}

	tyci := filepath.Join(dir, ".tyci")
	var conflicts []string
	kept := files[:0]
	for _, f := range files {
		if _, err := os.Lstat(filepath.Join(tyci, filepath.FromSlash(f.rel))); err == nil {
			if keep {
				continue
			}
			conflicts = append(conflicts, f.rel)
		}
		kept = append(kept, f)
	}
	files = kept
	cfgPath := filepath.Join(tyci, "config.json")
	cfg, roleConflicts, err := ejectConfig(cfgPath, prompted, keep)
	if err != nil {
		return nil, err
	}
	conflicts = append(conflicts, roleConflicts...)
	if len(conflicts) > 0 && !force {
		return nil, fmt.Errorf("these exist, use --force to overwrite: %s", strings.Join(conflicts, ", "))
	}

	var written []string
	for _, f := range files {
		p := filepath.Join(tyci, filepath.FromSlash(f.rel))
		if err := writeEjected(p, f.data, f.mode); err != nil {
			return written, err
		}
		written = append(written, p)
	}
	if cfg != nil {
		if err := writeEjected(cfgPath, cfg, 0o644); err != nil {
			return written, err
		}
		written = append(written, cfgPath)
	}
	return written, nil
}

// ejectConfig returns the new config.json with roles.<role>.prompt set to
// "@prompts/<role>.md" (nil when nothing changes) and the roles whose prompt
// is set to something else. With keep, a role prompt that is set stays as it
// is and is no conflict.
func ejectConfig(p string, roles []string, keep bool) (out []byte, conflicts []string, err error) {
	top := map[string]json.RawMessage{}
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, nil, err
	default:
		if err := json.Unmarshal(b, &top); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	all := map[string]map[string]json.RawMessage{}
	if raw, ok := top["roles"]; ok {
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, nil, fmt.Errorf("%s: roles: %w", p, err)
		}
	}
	changed := false
	for _, r := range roles {
		ref := "@prompts/" + r + ".md"
		role := all[r]
		if role == nil {
			role = map[string]json.RawMessage{}
		}
		var cur string
		if raw, ok := role["prompt"]; ok {
			_ = json.Unmarshal(raw, &cur)
		}
		if cur == ref {
			continue
		}
		if cur != "" && keep {
			continue
		}
		if cur != "" {
			conflicts = append(conflicts, "config.json roles."+r+".prompt")
		}
		v, _ := json.Marshal(ref)
		role["prompt"] = v
		all[r] = role
		changed = true
	}
	if !changed {
		return nil, conflicts, nil
	}
	if top["roles"], err = json.Marshal(all); err != nil {
		return nil, nil, err
	}
	out, err = json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return append(out, '\n'), conflicts, nil
}

// writeEjected replaces p. An old file or symlink is removed first, so a
// symlink is never followed.
func writeEjected(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(p, data, mode)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
