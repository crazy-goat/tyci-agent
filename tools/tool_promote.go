package tools

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/worktree"
)

// ToolPromoteTool implements "tool_promote": it copies a Lua tool made by
// tool_create into the project, at .tyci/tools/<name>.lua of the current
// worktree. A read-only oracle reviews the script first. Only a SAFE answer
// copies the file. Any other answer copies nothing, and the model asks the
// user. The tool never commits, pushes or merges.
type ToolPromoteTool struct{}

// toolPromoteName is the name of the tool. The role gate refers to it (see
// isRoleGatedTool).
const toolPromoteName = "tool_promote"

// promoteReasonLimit bounds the oracle text that is shown in a refusal.
const promoteReasonLimit = 300

//go:embed tool_promote_task.md
var promoteTaskTemplate string

// promoteSystemPrompt is the system prompt of the safety review.
const promoteSystemPrompt = "You are a security reviewer. You answer in the format the task gives, and you do not use tools."

// promoteOracle runs the safety review and returns its answer. It is a
// variable so that tests do not call a model. The review has a read-only
// policy with an empty tools list. It gets only the always-allowed tools
// (help, lua and report_progress). The read-only policy refuses read, write
// and bash, also through lua, so the review cannot read or write files or
// run commands.
var promoteOracle = func(ctx context.Context, task, dir string) (string, error) {
	res, _, err := RunSubagentTask(ctx, TaskSpec{
		Task:         task,
		SystemPrompt: promoteSystemPrompt,
		Dir:          dir,
		Policy:       &Policy{Role: "oracle", Permission: PermissionReadOnly, Tools: []string{}, Dir: dir},
	})
	return res, err
}

func (t *ToolPromoteTool) Name() string { return toolPromoteName }

func (t *ToolPromoteTool) Run(ctx context.Context, input map[string]any) ToolResult {
	name := strings.TrimSpace(stringParam(input, "name", ""))
	if !toolCreateNameRE.MatchString(name) || strings.HasPrefix(name, "mcp_") {
		return validationf("name %q is not valid; use 1-64 lowercase letters, digits and underscores, starting with a letter, and not starting with mcp_", name)
	}
	dir := RunDirFrom(ctx)
	if dir == "" {
		return failf("tool_promote needs a run folder, and this agent has none. Only the main chat and a workflow run have one")
	}
	// The script is read once. The review and the copy use these same bytes,
	// so the file cannot change between the review and the copy.
	source, err := os.ReadFile(filepath.Join(dir, "tools", name+".lua"))
	if errors.Is(err, os.ErrNotExist) {
		return failf("no Lua tool named %q was made in this run or session; create it with tool_create first", name)
	}
	if err != nil {
		return failf("cannot read the tool %q of this run: %v", name, err)
	}

	root, err := promoteRoot(ctx)
	if err != nil {
		return failf("%v", err)
	}
	target := filepath.Join(root, ".tyci", "tools", name+".lua")
	if _, err := os.Lstat(target); err == nil {
		return failf("%s already exists; remove it or choose another name", target)
	}

	answer, err := promoteOracle(ctx, strings.Replace(promoteTaskTemplate, "{{source}}", string(source), 1), root)
	if reason := promoteRefusal(answer, err); reason != "" {
		return notPromoted(reason)
	}

	if err := writeNewFile(root, target, source); err != nil {
		return failf("cannot copy the tool to %s: %v", target, err)
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf("Copied the tool %q to %s. It is not committed.", name, target),
	}
}

// promoteRoot returns the top level of the worktree that the call works in.
// It refuses a directory that is not a linked git worktree, and the main
// checkout. The main checkout has a .git directory; a linked worktree has a
// .git file.
func promoteRoot(ctx context.Context) (string, error) {
	dir := Workdir(ctx)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot read the working directory: %v", err)
		}
		dir = wd
	}
	root, err := worktree.Root(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("tool_promote needs a git worktree, and %s is not one", dir)
	}
	info, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil {
		return "", fmt.Errorf("cannot check the git directory of %s: %v", root, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("tool_promote does not write into the main checkout %s; run it in a worktree", root)
	}
	return root, nil
}

// promoteRefusal returns the reason to keep the tool out of the project, or
// "" when the review answered SAFE. The first line of the answer, trimmed,
// must be exactly SAFE.
func promoteRefusal(answer string, err error) string {
	if err != nil {
		return "the oracle failed: " + err.Error()
	}
	text := strings.TrimSpace(answer)
	if text == "" {
		return "the oracle gave no answer"
	}
	first, _, _ := strings.Cut(text, "\n")
	if strings.TrimSpace(first) == "SAFE" {
		return ""
	}
	// One line, so the refusal reads well in the chat.
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > promoteReasonLimit {
		text = string(r[:promoteReasonLimit]) + "..."
	}
	return "the oracle did not answer SAFE: " + text
}

// notPromoted is the result for a refusal of the review. The model reads the
// text and asks the user in the chat.
func notPromoted(reason string) ToolResult {
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: "not promoted: " + reason + "; ask the user",
	}
}

// writeNewFile writes data to target, which must be inside root and must not
// exist. O_EXCL keeps an existing file: the write is refused, not replaced.
// The folder of target is created here, after the review said SAFE, so a
// refusal leaves no folder behind.
func writeNewFile(root, target string, data []byte) error {
	if !within(root, target) {
		return fmt.Errorf("%s is outside the worktree", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(target)
		return err
	}
	return f.Close()
}
