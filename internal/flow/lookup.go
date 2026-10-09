package flow

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

var workflowName = regexp.MustCompile(`^[a-z0-9-]+$`)

// Lookup finds a workflow directory (SDR 5.2). Order: <projectDir>/.tyci/workflows/<name>/
// (only when trusted), then <home>/.tyci/workflows/<name>/. The first directory that
// exists wins. source is that directory. There is no fallback: a workflow that is not
// on disk is an error.
func Lookup(name, home, projectDir string, trusted bool) (wf *Workflow, source string, err error) {
	wf, source, err = findWorkflow(name, home, projectDir, trusted)
	if err != nil {
		return nil, "", err
	}
	if err := loadFiles(wf, source); err != nil {
		return nil, "", fmt.Errorf("%s: %w", source, err)
	}
	return wf, source, nil
}

// findWorkflow returns the workflow named name and its directory. It parses
// workflow.json but does not read the files that the workflow names (see loadFiles).
func findWorkflow(name, home, projectDir string, trusted bool) (*Workflow, string, error) {
	if !workflowName.MatchString(name) {
		return nil, "", fmt.Errorf("bad workflow name %q: use a-z, 0-9 and -", name)
	}
	for _, base := range lookupBases(home, projectDir, trusted) {
		dir := filepath.Join(base, ".tyci", "workflows", name)
		st, serr := os.Stat(dir)
		if errors.Is(serr, fs.ErrNotExist) {
			continue
		}
		if serr != nil {
			return nil, "", serr
		}
		if !st.IsDir() {
			return nil, "", fmt.Errorf("%s is not a directory", dir)
		}
		wf, err := loadWorkflow(name, dir)
		if err != nil {
			return nil, "", err
		}
		return wf, dir, nil
	}
	return nil, "", workflowNotFound(name, home, projectDir, trusted)
}

// lookupBases returns the directories that can hold .tyci/workflows, project first.
func lookupBases(home, projectDir string, trusted bool) []string {
	var bases []string
	if trusted && projectDir != "" {
		bases = append(bases, projectDir)
	}
	if home != "" {
		bases = append(bases, home)
	}
	return bases
}

// loadWorkflow reads <dir>/workflow.json and checks its name. It does not read the
// files that the workflow names (see loadFiles).
func loadWorkflow(name, dir string) (*Workflow, error) {
	data, err := os.ReadFile(filepath.Join(dir, "workflow.json"))
	if err != nil {
		return nil, err
	}
	wf, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if wf.Name == "" {
		wf.Name = name
	} else if wf.Name != name {
		return nil, fmt.Errorf("%s: name %q must equal the directory name %q", dir, wf.Name, name)
	}
	wf.Source = dir
	return wf, nil
}

// loadFiles checks the check scripts and reads the task templates and the prompts of
// the workflow in dir. Every missing or unreadable file is reported, in one error.
func loadFiles(wf *Workflow, dir string) error {
	return errors.Join(checkScripts(wf, dir), loadTexts(wf, dir))
}

// checkScripts returns one error for each check script of wf that dir does not hold.
func checkScripts(wf *Workflow, dir string) error {
	var errs []error
	for _, name := range sortedStates(wf) {
		if s := wf.States[name]; s.Check != "" {
			if _, err := ResolveCheck(s.Check, dir); err != nil {
				errs = append(errs, fmt.Errorf("state %q: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// loadTexts reads the task templates and the @file state prompts of wf from dir.
// Role prompts in <dir>/prompts/<role>.md become the prompt of the agent states of
// that role, unless the state has its own.
func loadTexts(wf *Workflow, dir string) error {
	var errs []error
	for _, name := range sortedStates(wf) {
		s := wf.States[name]
		if s.Task != "" && s.Agent != "" {
			if _, err := flowconfig.ReadPromptFile(dir, taskFile(s.Task)); err != nil {
				errs = append(errs, fmt.Errorf("state %q: task: %w", name, err))
			}
		}
		if rel, ok := strings.CutPrefix(s.Prompt, "@"); ok {
			text, err := flowconfig.ReadPromptFile(dir, rel)
			if err != nil {
				errs = append(errs, fmt.Errorf("state %q: %w", name, err))
				continue
			}
			s.Prompt = text
			wf.States[name] = s
		}
		if s.Agent == "" || s.Prompt != "" {
			continue
		}
		// A role prompt file is optional: without it the config prompt stays.
		text, err := flowconfig.ReadPromptFile(dir, "prompts/"+s.Agent+".md")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("state %q: role prompt: %w", name, err))
			continue
		}
		s.Prompt = text
		wf.States[name] = s
	}
	return errors.Join(errs...)
}

// taskFile is the path of a task template, relative to the workflow directory.
func taskFile(task string) string { return "tasks/" + task + ".md" }

// workflowNotFound is the error of an unknown workflow. It lists the workflows that
// exist, names a hint, and says why a project workflow is not used.
func workflowNotFound(name, home, projectDir string, trusted bool) error {
	var notes []string
	if !trusted && projectDir != "" {
		if _, err := os.Stat(filepath.Join(projectDir, ".tyci", "workflows", name)); err == nil {
			notes = append(notes, fmt.Sprintf("%s is in this project, but the project is not trusted, so it is ignored",
				filepath.Join(".tyci", "workflows", name)))
		}
	}
	for _, base := range []string{projectDir, home} {
		if base == "" {
			continue
		}
		old := filepath.Join(base, ".tyci", "workflows", name+".json")
		if _, err := os.Stat(old); err == nil {
			notes = append(notes, fmt.Sprintf("found the old file %s; move it to %s", old,
				filepath.Join(base, ".tyci", "workflows", name, "workflow.json")))
		}
	}
	msg := fmt.Sprintf("workflow %q not found", name)
	if names := availableWorkflows(home, projectDir, trusted); len(names) > 0 {
		msg += " (available: " + strings.Join(names, ", ") + ")"
	} else {
		msg += " (no workflows are available)"
	}
	hint := "issue-to-merge"
	if slices.Contains(Templates(), name) {
		hint = name
	}
	msg += fmt.Sprintf(`; run "tyci workflow init %s" to create one from a template`, hint)
	for _, n := range notes {
		msg += ". " + n
	}
	return errors.New(msg)
}

// availableWorkflows returns the sorted names of the workflow directories that Lookup
// searches.
func availableWorkflows(home, projectDir string, trusted bool) []string {
	seen := map[string]bool{}
	for _, base := range lookupBases(home, projectDir, trusted) {
		entries, err := os.ReadDir(filepath.Join(base, ".tyci", "workflows"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && workflowName.MatchString(e.Name()) {
				seen[e.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ProjectHasWorkflows reports whether the git repository of dir has a
// .tyci/workflows directory. tyci does not use it in an untrusted project.
// It uses the same toplevel as DetectRepoAt, so a linked worktree is checked
// in its own directory, as Lookup does.
func ProjectHasWorkflows(dir string) bool {
	root, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	st, err := os.Stat(filepath.Join(root, ".tyci", "workflows"))
	return err == nil && st.IsDir()
}
