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
	allowed := make(map[string]struct{}, len(names)+len(alwaysAllowedTools))
	permitted := make([]string, 0, len(names)+len(alwaysAllowedTools))
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
