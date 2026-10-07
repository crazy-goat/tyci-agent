package flow

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// embedded holds the builtin workflows, check scripts and task templates.
//
//go:embed builtin/*.json checks/*.sh tasks/*.md
var embedded embed.FS

// Embedded returns the embedded files, for ResolveCheck.
func Embedded() fs.FS { return embedded }

var workflowName = regexp.MustCompile(`^[a-z0-9-]+$`)

// Lookup finds a workflow (SDR 5.2). Order: <projectDir>/.tyci/workflows
// (only when trusted), <home>/.tyci/workflows, then the embedded copy.
// source is the file path or "builtin".
func Lookup(name, home, projectDir string, trusted bool) (wf *Workflow, source string, err error) {
	if !workflowName.MatchString(name) {
		return nil, "", fmt.Errorf("bad workflow name %q: use a-z, 0-9 and -", name)
	}
	var dirs []string
	if trusted && projectDir != "" {
		dirs = append(dirs, projectDir)
	}
	if home != "" {
		dirs = append(dirs, home)
	}
	for _, d := range dirs {
		p := filepath.Join(d, ".tyci", "workflows", name+".json")
		data, rerr := os.ReadFile(p)
		if os.IsNotExist(rerr) {
			continue
		}
		if rerr != nil {
			return nil, "", rerr
		}
		if wf, err = Parse(data); err != nil {
			return nil, "", fmt.Errorf("%s: %w", p, err)
		}
		if err = readStatePrompts(wf, filepath.Join(d, ".tyci")); err != nil {
			return nil, "", fmt.Errorf("%s: %w", p, err)
		}
		wf.Source = p
		return wf, p, nil
	}
	data, rerr := embedded.ReadFile("builtin/" + name + ".json")
	if rerr != nil {
		return nil, "", fmt.Errorf("workflow %q not found", name)
	}
	if wf, err = Parse(data); err != nil {
		return nil, "", err
	}
	wf.Source = "builtin"
	return wf, "builtin", nil
}

// readStatePrompts replaces every state prompt "@<file>" with the text of
// <tyciDir>/<file>, with the rules of a role prompt file.
func readStatePrompts(wf *Workflow, tyciDir string) error {
	for name, s := range wf.States {
		rel, ok := strings.CutPrefix(s.Prompt, "@")
		if !ok {
			continue
		}
		text, err := flowconfig.ReadPromptFile(tyciDir, rel)
		if err != nil {
			return fmt.Errorf("state %q: %w", name, err)
		}
		s.Prompt = text
		wf.States[name] = s
	}
	return nil
}
