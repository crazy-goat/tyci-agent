package flow

import (
	"bytes"
	"fmt"
	"regexp"
	"text/template"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

// TaskData is the only data a task template can read.
type TaskData struct {
	Repo, Branch, DefaultBranch, Worktree, RunDir, Reason string
	Workflow                                              string // workflow name
	Input                                                 string // roadmap oracle input JSON
	Failed, FailedKey, FailedDir                          string // the failed check step (fixer, recover)
	Issue, PR, Visit                                      int
}

func renderTaskText(name, text string, d TaskData) (string, error) {
	t, err := template.New(name).Option("missingkey=error").Funcs(template.FuncMap{}).Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return "", err
	}
	return buf.String(), nil
}

var taskName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// TaskTemplates implements TaskRenderer. A task is read from <Dir>/tasks/<name>.md,
// where Dir is the workflow directory.
type TaskTemplates struct {
	Dir string
}

// Render implements TaskRenderer. It masks Reason again, so a caller
// cannot leak a secret by mistake.
func (t TaskTemplates) Render(name string, rc RunContext) (string, error) {
	d := TaskData{
		Workflow: rc.Workflow,
		Repo:     rc.Repo, Branch: rc.Branch, DefaultBranch: rc.DefaultBranch,
		Worktree: rc.Worktree, RunDir: rc.RunDir, Reason: MaskSecrets(rc.Reason),
		Issue: rc.Issue, PR: rc.PR, Visit: rc.Visit, Input: rc.Input,
		Failed: rc.Failed, FailedKey: rc.FailedKey, FailedDir: rc.FailedDir,
	}
	if !taskName.MatchString(name) {
		return "", fmt.Errorf("bad task name %q: use a-z, 0-9, _ and -", name)
	}
	text, err := flowconfig.ReadPromptFile(t.Dir, taskFile(name))
	if err != nil {
		return "", fmt.Errorf("task %q: %w", name, err)
	}
	return renderTaskText(name, text, d)
}
