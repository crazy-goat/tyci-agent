package flow

import (
	"bytes"
	"fmt"
	"text/template"
)

// TaskData is the only data a task template can read.
type TaskData struct {
	Repo, Branch, DefaultBranch, Worktree, RunDir, Reason string
	Issue, PR, Visit                                      int
}

// RenderTask renders the embedded task template name (without extension).
func RenderTask(name string, d TaskData) (string, error) {
	b, err := embedded.ReadFile("tasks/" + name + ".md")
	if err != nil {
		return "", fmt.Errorf("unknown task %q", name)
	}
	return renderTaskText(name, string(b), d)
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

// TaskTemplates implements TaskRenderer with the embedded templates.
type TaskTemplates struct{}

// Render implements TaskRenderer. It masks Reason again, so a caller
// cannot leak a secret by mistake.
func (TaskTemplates) Render(name string, rc RunContext) (string, error) {
	return RenderTask(name, TaskData{
		Repo: rc.Repo, Branch: rc.Branch, DefaultBranch: rc.DefaultBranch,
		Worktree: rc.Worktree, RunDir: rc.RunDir, Reason: MaskSecrets(rc.Reason),
		Issue: rc.Issue, PR: rc.PR, Visit: rc.Visit,
	})
}
