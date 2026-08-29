package tools

import "context"

// Nesting depth.
//
// Item 21 ("Grandchildren") lets a narrow, read-only "scout" nest a few
// levels beyond where a plain "subagent" is allowed to run at all, on the
// premise that depth alone is not the safety property — a scout is exactly
// as safe at depth 4 as it is at depth 2, because it registers no job, is
// synchronous, and inherits its caller's remaining deadline instead of
// resetting one. What differs by depth is only which delegation tool (if
// any) a caller may reach — see AllowedDelegationTool/ToolAllowedAtDepth in
// toolgate.go — and that requires depth to travel with the call the same
// way Workdir and SubagentSinkCtxKey already do (see workdir.go).
//
// Depth 0 is the top-level conversation. Nothing set in the context is
// exactly that: the zero value of the type read back below, so every
// existing context.Background() (production top-level contexts, and every
// test that never calls WithDepth) means depth 0 without having to say so.
type DepthCtxKey struct{}

// WithDepth returns a context carrying depth for DepthFromContext to read
// back. Set by runSingleTask (tools/subagent.go), right next to
// SubagentSinkCtxKey, as the caller's own depth (DepthFromContext(ctx) on
// the context runSingleTask was itself called with) plus one — so a
// subagent spawned from the top level lands at depth 1, a scout spawned
// from that subagent's own tool calls lands at depth 2, and so on.
func WithDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, DepthCtxKey{}, depth)
}

// DepthFromContext returns the nesting depth ctx carries, or 0 (top level)
// when none was set.
func DepthFromContext(ctx context.Context) int {
	d, _ := ctx.Value(DepthCtxKey{}).(int)
	return d
}
