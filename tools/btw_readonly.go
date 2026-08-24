package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// BtwReadOnlyGate limits the evaluation phase of a /btw side-conversation to
// tools that only inspect state. The side conversation is deliberately not a
// subagent yet: it must be able to decide whether work is worth delegating
// without changing files, scheduling work, or mutating shared agent state.
//
// This is separate from AllowOnly because that helper intentionally adds the
// always-allowed lua tool. Lua can dispatch write and other mutating tools, so
// it must not be part of this gate. Unknown and MCP tools are denied too: an
// MCP server's capabilities cannot be established from its name alone.
func BtwReadOnlyGate() ToolGate {
	allowed := map[string]struct{}{
		"agents": {},
		"find":   {},
		"help":   {},
		"read":   {},
		"skills": {},
		"web":    {},
	}
	permitted := make([]string, 0, len(allowed))
	for name := range allowed {
		permitted = append(permitted, name)
	}
	sort.Strings(permitted)
	list := strings.Join(permitted, ", ")
	return func(name string) error {
		if _, ok := allowed[name]; ok {
			return nil
		}
		return fmt.Errorf("tool %q is not available during /btw evaluation; read-only tools are: %s", name, list)
	}
}

// BtwEvaluationSchemaJSON returns the tools exposed before a /btw side
// conversation has been promoted. Keeping the schema and runtime gate in one
// helper avoids presenting mutating tools that the gate will reject.
func BtwEvaluationSchemaJSON() []byte {
	all := GetAllToolsSchema()
	gate := BtwReadOnlyGate()
	filtered := make([]map[string]any, 0, len(all))
	for _, schema := range all {
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := fn["name"].(string)
		if !ok {
			continue
		}
		if gate(name) == nil {
			filtered = append(filtered, schema)
		}
	}
	data, _ := json.Marshal(filtered)
	return data
}

// JobPromoter starts one real subagent from a completed read-only /btw
// transcript. The tools package owns only this small interface; main wires the
// registry-backed implementation.
type JobPromoter interface {
	Promote(ctx context.Context, jobID string) (JobHandle, error)
}

var jobPromoter JobPromoter

func SetJobPromoter(p JobPromoter) { jobPromoter = p }

// PromoteBtwTool is intentionally available only to the parent/main schema.
// The evaluator cannot call it because its read-only runtime gate denies it.
type PromoteBtwTool struct{}

func (t *PromoteBtwTool) Name() string { return "promote_btw" }
func (t *PromoteBtwTool) Run(ctx context.Context, input map[string]any) ToolResult {
	id, _ := input["job_id"].(string)
	if id == "" {
		return ToolResult{Type: "result", Success: false, Error: "job_id is required"}
	}
	if jobPromoter == nil {
		return ToolResult{Type: "result", Success: false, Error: "promote_btw unavailable: job registry not configured"}
	}
	h, err := jobPromoter.Promote(ctx, id)
	if err != nil {
		return ToolResult{Type: "result", Success: false, Error: err.Error()}
	}
	return ToolResult{
		Type:    "result",
		Success: true,
		Content: fmt.Sprintf("{\"job_id\":%q}\nA real subthread now exists as job %s. Wait for its result with wait(job_id=%q); do not redo its work in this thread.", h.ID(), h.ID(), h.ID()),
	}
}
