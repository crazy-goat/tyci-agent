package flow

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// templates holds the workflow templates that Init copies, their check scripts and
// their task templates. A template is never run in place.
//
//go:embed templates/*.json checks/*.sh tasks/*.md
var templates embed.FS

// Templates returns the names of the workflow templates, sorted.
func Templates() []string {
	matches, err := fs.Glob(templates, "templates/*.json")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(path.Base(m), ".json"))
	}
	slices.Sort(names)
	return names
}

// Init copies the template into <dir>/.tyci/workflows/<name>/: workflow.json, all
// check scripts (they source each other), the task templates of its agent states and
// the prompts of its builtin roles in prompts/. An empty name means the template name.
// It creates the directory with os.Mkdir, so an existing workflow is never overwritten.
// It does not touch config.json. It returns the written paths.
func Init(template, name, dir string) ([]string, error) {
	if !slices.Contains(Templates(), template) {
		return nil, fmt.Errorf("unknown template %q (templates: %s)", template, strings.Join(Templates(), ", "))
	}
	if name == "" {
		name = template
	}
	if !workflowName.MatchString(name) {
		return nil, fmt.Errorf("bad workflow name %q: use a-z, 0-9 and -", name)
	}
	data, err := templates.ReadFile("templates/" + template + ".json")
	if err != nil {
		return nil, err
	}
	wf, err := Parse(data)
	if err != nil {
		return nil, err
	}
	files, err := templateFiles(wf, data)
	if err != nil {
		return nil, err
	}

	root := filepath.Join(dir, ".tyci", "workflows", name)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return nil, err
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%s already exists: choose another name or remove the directory", root)
		}
		return nil, err
	}
	var written []string
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := writeNew(p, f.data, f.mode); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
		written = append(written, p)
	}
	return written, nil
}

type templateFile struct {
	rel  string // relative to the workflow directory
	data []byte
	mode os.FileMode
}

// templateFiles returns the files of a template, relative to its workflow directory.
func templateFiles(wf *Workflow, data []byte) ([]templateFile, error) {
	files := []templateFile{{rel: "workflow.json", data: data, mode: 0o644}}
	checks, err := fs.Glob(templates, "checks/*.sh")
	if err != nil {
		return nil, err
	}
	for _, c := range checks {
		b, err := templates.ReadFile(c)
		if err != nil {
			return nil, err
		}
		files = append(files, templateFile{rel: c, data: b, mode: 0o755})
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
		b, err := templates.ReadFile("tasks/" + t + ".md")
		if err != nil {
			return nil, fmt.Errorf("task %q: %w", t, err)
		}
		files = append(files, templateFile{rel: taskFile(t), data: b, mode: 0o644})
	}
	for _, r := range sortedSet(roles) {
		if p, ok := flowconfig.DefaultPrompt(r); ok {
			files = append(files, templateFile{rel: "prompts/" + r + ".md", data: []byte(p), mode: 0o644})
		}
	}
	return files, nil
}

// writeNew writes a new file p and creates its parent directories.
func writeNew(p string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, mode)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
