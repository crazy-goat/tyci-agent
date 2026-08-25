package tools

import (
	"context"
	"fmt"
	"strings"
)

// Compactor is installed by the conversation owner for a running agent.
// It returns the path to the derived dump and replaces the live history with
// the compacted view; the raw JSONL is retained by the owner.
type Compactor func(summary, focus string) (dumpPath string, err error)

type compactorCtxKey struct{}

// WithCompactor makes the model-facing compact tool available for one agent
// turn. A missing compactor is an honest error (for example in a no-session
// run), rather than a pretend successful compaction.
func WithCompactor(ctx context.Context, c Compactor) context.Context {
	return context.WithValue(ctx, compactorCtxKey{}, c)
}

func compactorFrom(ctx context.Context) Compactor {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(compactorCtxKey{}).(Compactor)
	return c
}

// CompactTool asks the session owner to summarize the current history.
type CompactTool struct{}

func (t *CompactTool) Name() string { return "compact" }

func (t *CompactTool) MaxParallel() int { return 1 }

func (t *CompactTool) Run(ctx context.Context, input map[string]any) ToolResult {
	summary := strings.TrimSpace(stringParam(input, "summary", ""))
	if summary == "" {
		return ToolResult{Type: "result", Success: false, Error: "compact requires a non-empty summary — persist anything important first, then call compact(summary=\"...\")"}
	}
	focus := strings.TrimSpace(stringParam(input, "focus", ""))
	c := compactorFrom(ctx)
	if c == nil {
		return ToolResult{Type: "result", Success: false, Error: "compact is unavailable: this conversation has no writable session"}
	}
	path, err := c(summary, focus)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: fmt.Sprintf("compaction failed: %v", err)}
	}
	return ToolResult{Type: "result", Success: true, Content: fmt.Sprintf("history compacted; the complete raw record is available at %s", path)}
}
