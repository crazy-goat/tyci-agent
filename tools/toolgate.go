package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Tool permission gating.
//
// Some callers may use only a subset of the tool registry. The clearest case
// is a child agent: it must never spawn further children, and a named agent
// definition may restrict it to a handful of tools. Until scripts could call
// tools, enforcing that above RunTool was enough — main.go's
// subagentToolRunner checked the name and then dispatched.
//
// The "lua" tool broke that arrangement: a script calls RunTool directly, so
// a restriction applied by the layer above is simply not in the path. Rather
// than teach every such caller about subagents (which this package must not
// know about — it cannot import the agent layer), the restriction travels
// with the context, and RunTool applies it. Anything that dispatches a tool
// inside the tools package is then covered for free, including whatever gets
// added later.

type toolGateCtxKey struct{}

// ToolGate reports whether name may be invoked, returning an error explaining
// the refusal in terms the model can act on.
type ToolGate func(name string) error

// WithToolGate returns a context in which only the tools gate approves may be
// run. Gates nest: an inner gate is checked after the outer one, so a
// restriction can be narrowed but never widened.
func WithToolGate(ctx context.Context, gate ToolGate) context.Context {
	if gate == nil {
		return ctx
	}
	if outer := toolGateFrom(ctx); outer != nil {
		inner := gate
		gate = func(name string) error {
			if err := outer(name); err != nil {
				return err
			}
			return inner(name)
		}
	}
	return context.WithValue(ctx, toolGateCtxKey{}, gate)
}

func toolGateFrom(ctx context.Context) ToolGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(toolGateCtxKey{}).(ToolGate)
	return gate
}

// checkToolGate returns an error when ctx forbids running name.
func checkToolGate(ctx context.Context, name string) error {
	gate := toolGateFrom(ctx)
	if gate == nil {
		return nil
	}
	return gate(name)
}

// AllowOnly builds a gate permitting exactly the named tools. An empty list
// means "no restriction", matching how a named agent's frontmatter `tools:`
// field is read: absent means unrestricted, not "nothing allowed".
func AllowOnly(names ...string) ToolGate {
	if len(names) == 0 {
		return nil
	}
	return newAllowGate(names)
}

// AllowOnlySubagent builds the runtime gate for a subagent's tools:
// whitelist (a named agent definition's frontmatter list). It mirrors
// GetSubagentToolsSchemaJSONFor tool for tool — same "empty/nil means no
// restriction" convention, same subagentDeniedTools filtering, same
// alwaysAllowedTools folding — so a call this gate permits is always one
// the schema for the same allowed list actually offered the model, and a
// call the schema never offered is always one this gate refuses.
//
// Filtering happens even for an allowed list that, after removing
// subagentDeniedTools entries, has nothing left: unlike a bare
// AllowOnly(filtered...) call, an empty result here does NOT fall through
// to "no restriction" — that would let an agent whose entire tools: list
// happened to be denied names (e.g. tools: [agents]) end up with a runtime
// gate wider than the schema it was shown. It gets exactly
// alwaysAllowedTools instead, matching what GetSubagentToolsSchemaJSONFor
// would build for the same input.
func AllowOnlySubagent(allowed []string) ToolGate {
	if len(allowed) == 0 {
		return nil
	}
	return newAllowGate(FilterSubagentDenied(allowed))
}

// newAllowGate builds a gate permitting names plus alwaysAllowedTools, plus
// any MCP tool covered by an mcp_<server>_* wildcard present in names (see
// mcpAllowedByWildcard in tools/mcp.go) — the runtime-gate half of the same
// opt-in GetSubagentToolsSchemaJSONFor implements on the schema side. A
// literal mcp_<server>_<tool> entry in names is already granted by the
// plain map lookup below; the wildcard is the one case that needs an extra
// check, since the exact dynamic tool name can't have been in names when
// the agent definition was written. names may be empty (AllowOnlySubagent
// relies on this: every entry in a subagent's tools: list can turn out to
// be a denied tool, and the gate must still come out non-nil, permitting
// exactly alwaysAllowedTools).
func newAllowGate(names []string) ToolGate {
	allowed := make(map[string]struct{}, len(names)+len(alwaysAllowedTools))
	permitted := make([]string, 0, len(names)+len(alwaysAllowedTools))
	wildcards := append([]string(nil), names...)
	for _, n := range names {
		if _, dup := allowed[n]; dup {
			continue
		}
		allowed[n] = struct{}{}
		permitted = append(permitted, n)
	}
	for _, n := range alwaysAllowedTools {
		if _, dup := allowed[n]; dup {
			continue
		}
		allowed[n] = struct{}{}
		permitted = append(permitted, n)
	}
	sort.Strings(permitted)
	list := strings.Join(permitted, ", ")

	return func(name string) error {
		if _, ok := allowed[name]; ok {
			return nil
		}
		if mcpAllowedByWildcard(name, wildcards) {
			return nil
		}
		// The refusal names the alternatives. Telling an agent only what it
		// cannot do leaves it guessing at what it can, and a guess costs
		// another refused call.
		return fmt.Errorf("tool %q is not available to this agent. You have: %s. Solve the task with those, or say in your result that you could not", name, list)
	}
}

// alwaysAllowedTools are available to every agent whatever its definition
// says, because withholding them cannot make an agent safer, only worse at
// its job:
//
//   - help, so a refusal or a surprise is answerable. An agent that cannot
//     look a tool up guesses instead, and every guess costs a refused call.
//   - lua, because it grants no new powers: tool() inside a script goes
//     through RunTool, which checks this very gate, so a restricted agent's
//     script is restricted identically. What it does grant is the ability to
//     do twenty greps in one round trip instead of twenty — and a narrow
//     agent like "locator" is exactly the one that needs that most.
var alwaysAllowedTools = []string{"help", "lua"}

// subagentDeniedTools names tools that are never available to a subagent,
// whatever its own tools: whitelist says:
//
//   - subagent: recursion into further children is never permitted.
//   - agents: its only purpose is discovering names for
//     subagent(agent="name"); a child that cannot call subagent at all has
//     nothing to do with that list.
//   - answer_job: relays an answer to a job blocked in ask_parent. A plain
//     subagent child is always denied "subagent" above, so it can never
//     spawn a grandchild — and with no descendant able to ever call
//     ask_parent, a blocked-on-ask_parent job is never something this
//     child could be asked to unblock. Grouped here for the same
//     mechanical reason as "agents" (dead weight, not a safety concern),
//     even though the underlying reasoning differs from "subagent"/
//     "agents" (recursion/discovery vs. "cannot possibly have a use for
//     this"). NOTE: this stops being true once item 21 (nested subagents /
//     "scout") lands — a depth-2+ agent that CAN spawn further children
//     would need answer_job back even though it's still, at that point, "a
//     subagent". When that lands, this entry likely needs to move to a
//     depth-aware check rather than a flat denial.
//   - message: posts to a running job's mailbox, steering it mid-flight.
//     Same reasoning as answer_job — a plain subagent child can never have
//     a job of its own to message, since it is always denied "subagent"
//     above. Revisit alongside answer_job once item 21 lands.
//   - request_timeout_extension: subagents have no execution deadline, so a
//     request to extend one would always be rejected as inapplicable.
//
// This is the one place the schema builder
// (GetSubagentToolsSchema/GetSubagentToolsSchemaJSONFor in tool.go), the
// whitelisted runtime gate (AllowOnlySubagent/FilterSubagentDenied below),
// AND the unrestricted runtime gate (DenySubagentRecursion, used by
// main.go's subagentToolRunner.Run when a child has no tools: whitelist at
// all) all read this from — so a name denied in the schema cannot quietly
// stay permitted at either runtime gate, or vice versa. Every one of these
// three call sites must consult subagentDeniedTools rather than hard-coding
// "subagent" or "agents" by name, or the three will drift the way
// AllowOnlySubagent's whitelisted path and this package's own unrestricted
// path once did.
var subagentDeniedTools = map[string]bool{"subagent": true, "agents": true, "answer_job": true, "message": true, "promote_btw": true, "resume": true, "request_timeout_extension": true}

// IsSubagentDenied reports whether name is one of subagentDeniedTools.
func IsSubagentDenied(name string) bool {
	return subagentDeniedTools[name]
}

// FilterSubagentDenied returns names with every subagentDeniedTools entry
// removed, preserving order. Callers building a per-agent AllowOnly gate
// from a frontmatter tools: list should filter through this first, so an
// agent definition that lists "agents" (or, redundantly, "subagent") cannot
// have the runtime gate permit a call the schema never offered.
func FilterSubagentDenied(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if IsSubagentDenied(n) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// SubagentDeniedNames returns the subagentDeniedTools entries as a sorted
// slice, for callers that need to hand the list to something that takes
// names rather than a membership test (e.g. Deny below).
func SubagentDeniedNames() []string {
	names := make([]string, 0, len(subagentDeniedTools))
	for n := range subagentDeniedTools {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// DenySubagentRecursion builds a gate refusing every subagentDeniedTools
// entry ("subagent", "agents") and allowing everything else. It is the
// unrestricted-child counterpart to AllowOnlySubagent: a child with no
// tools: whitelist at all (main.go's subagentToolRunner.Run when
// r.allowed is empty) still must not reach a tool
// GetSubagentToolsSchemaJSON never offered it — previously only "subagent"
// was denied on that path, which let an unrestricted child reach "agents"
// (directly, or via lua's tool("agents", {})) even though the schema never
// listed it.
func DenySubagentRecursion() ToolGate {
	return Deny("tool is not available to subagents (recursion/discovery denied)", SubagentDeniedNames()...)
}

// Deny builds a gate refusing the named tools and allowing everything else.
func Deny(reason string, names ...string) ToolGate {
	denied := make(map[string]struct{}, len(names))
	for _, n := range names {
		denied[n] = struct{}{}
	}
	return func(name string) error {
		if _, ok := denied[name]; ok {
			return fmt.Errorf("%s", reason)
		}
		return nil
	}
}
