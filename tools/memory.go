package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/internal/instructions"
)

// MemoryTool implements the "memory" tool: short notes the agent writes for
// its own future sessions, stored as markdown files under .tyci/memory/ and
// injected into the system prompt at the start of every session.
//
// It exists because a session's own history is the only thing an agent
// remembers, and that history is thrown away when the session ends. Anything
// worked out the hard way — which command actually runs the tests, why a
// package must not import another, where the real entry point is — has to be
// worked out again next time, or explained again by the person. A note is the
// cheap way out of that loop.
//
// Deliberately small: four actions, one flat namespace, no directories. The
// value is in what gets written, not in the filing system, and every note is
// re-sent with every request for the rest of the project's life.
type MemoryTool struct{}

func (t *MemoryTool) Name() string { return "memory" }

func (t *MemoryTool) Run(ctx context.Context, input map[string]any) ToolResult {
	cwd, err := os.Getwd()
	if err != nil {
		return failf("cannot determine the working directory: %v", err)
	}

	action := strings.TrimSpace(stringParam(input, "action", ""))
	name := stringParam(input, "name", "")
	content := stringParam(input, "content", "")

	switch action {
	case "list", "":
		return t.list(cwd)
	case "read":
		if name == "" {
			return validationf("name is required for action=\"read\"")
		}
		body, err := instructions.Read(cwd, name)
		if err != nil {
			if errors.Is(err, instructions.ErrValidation) {
				return validationf("%v", err)
			}
			return failf("%v", err)
		}
		return okf("%s", body)
	case "write":
		if name == "" {
			return validationf("name is required for action=\"write\" — a short slug like \"test-command\" or \"layering-rules\"")
		}
		stored, err := instructions.Write(cwd, name, content)
		if err != nil {
			if errors.Is(err, instructions.ErrValidation) {
				return validationf("%v", err)
			}
			return failf("%v", err)
		}
		// The note is already on disk, but the system prompt for THIS session
		// was built before it existed. Saying so stops the model concluding
		// the write failed when it cannot see the note in its own context.
		return okf("saved note %q. It is loaded into the system prompt at the start of a session, so it will be there next time rather than appearing in this conversation.", stored)
	case "delete":
		if name == "" {
			return validationf("name is required for action=\"delete\"")
		}
		removed, err := instructions.Delete(cwd, name)
		if err != nil {
			if errors.Is(err, instructions.ErrValidation) {
				return validationf("%v", err)
			}
			return failf("%v", err)
		}
		return okf("deleted note %q", removed)
	default:
		return validationf("unknown action %q; use \"list\", \"read\", \"write\" or \"delete\"", action)
	}
}

func (t *MemoryTool) list(cwd string) ToolResult {
	names, err := instructions.List(cwd)
	if err != nil {
		return failf("cannot read %s: %v", instructions.MemoryDirName, err)
	}
	if len(names) == 0 {
		return okf("no notes yet. Write one with memory(action=\"write\", name=\"...\", content=\"...\") when you learn something about this project that is not obvious from the code and would otherwise have to be worked out again.")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d notes in %s (all of them are already in your system prompt):", len(names), instructions.MemoryDirName)
	for _, n := range names {
		b.WriteString("\n- ")
		b.WriteString(n)
	}
	return okf("%s", b.String())
}

func okf(format string, args ...any) ToolResult {
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf(format, args...)}
}

func failf(format string, args ...any) ToolResult {
	return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf(format, args...)}
}

func validationf(format string, args ...any) ToolResult {
	return validationResult(fmt.Sprintf(format, args...))
}
