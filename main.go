package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/tools"
)

// agentRunner implements tools.SubAgentRunner by wrapping agent.Run.
type agentRunner struct{}

// resolveModelClient picks the resolved model client for a subagent.
//
// An explicit "provider/model" override is resolved via the registry. Otherwise
// the subagent inherits the parent's model client from context — which is
// already configured with a valid API key — instead of re-guessing via
// FindModel, whose bare-name lookup iterates the provider map in random order
// and can land on a different (unconfigured) provider that happens to list
// the same model.
func resolveModelClient(ctx context.Context, model string) (connector.ModelClient, error) {
	if strings.Contains(model, "/") {
		if prov, mName, ok := providers.FindModel(model); ok {
			return prov.Client(mName), nil
		}
		return nil, fmt.Errorf("no provider available for model %q", model)
	}

	mc := connector.ModelClientFromContext(ctx)
	if mc == nil {
		// No parent model client in context (e.g. tests) — fall back to lookup.
		if p, m, ok := providers.FindModel(model); ok {
			return p.Client(m), nil
		}
		return nil, fmt.Errorf("no provider available for model %q", model)
	}
	mName := model
	if mName == "" {
		mName = mc.Model()
	}
	if mName == "" {
		return nil, fmt.Errorf("no model specified")
	}
	if mName == mc.Model() {
		return mc, nil
	}
	// Explicit bare-name override that differs from the parent's default:
	// keep the parent's provider (its already-resolved credential), bound to
	// the new model. The provider must be registered under its own name in
	// the catalog for this lookup to succeed — true for every real provider,
	// each registered exactly once at startup.
	prov, ok := providers.GetProvider(mc.Provider())
	if !ok {
		return nil, fmt.Errorf("provider %q not found", mc.Provider())
	}
	return prov.Client(mName), nil
}

// withIsolatedPool binds mc, and every entry in fallbacks, to ONE HTTP client
// with its own connection pool, so a child agent shares nothing with its
// parent: parent cancellation cannot leak into subagent requests and vice
// versa. Primary and fallback share the pool because within a single child
// run they are never used concurrently — agent.Run tries them one after
// another, never in parallel.
//
// This used to live in tools/subagent.go, which stuffed the client into the
// child's context under an api-package context key. The transport is not
// something the tools package should know about, and the api layer no longer
// reads the context at all; a client now carries its own transport instead.
//
// Etap 5 (docs/architecture-refactor.md) closed a latent gap here: before the
// caller resolved fallbacks, agent/fallback.go pulled a fresh provider from
// the global catalog mid-run, invisibly to this wrapper — a fallback
// triggered inside a child run would have silently fallen back to the shared
// api.defaultClient instead of the child's isolated pool. Now that the
// caller resolves every fallback up front, it can wrap them together with
// the primary in the same call, so the gap cannot reopen without also
// changing this function.
//
// Granularity is otherwise unchanged: agentRunner.run is entered exactly once
// per tools.SubAgentRunner.RunTask/RunTaskWithSystem call, i.e. once per
// runSingleTask, so a parallel subagent(tasks=[a,b,c]) still creates three
// pools — one per child.
//
// A ModelClient that does not implement connector.HTTPInjector (every fake in
// the test suite) is returned untouched and keeps today's "no isolation"
// behavior. That fallback used to be the whole injection path's default
// failure mode, because it ran through three interfaces and a type assertion
// at each hop. It is now a single hop: every client the providers package
// hands out implements connector.HTTPInjector, and providers/client.go asserts
// that at BUILD time, so the assertion below cannot start failing silently
// for production clients.
func withIsolatedPool(mc connector.ModelClient, fallbacks []connector.ModelClient) (connector.ModelClient, []connector.ModelClient) {
	pool := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        2,
			MaxIdleConnsPerHost: 1,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	bind := func(c connector.ModelClient) connector.ModelClient {
		inj, ok := c.(connector.HTTPInjector)
		if !ok {
			return c
		}
		return inj.WithHTTP(pool)
	}
	boundFallbacks := make([]connector.ModelClient, len(fallbacks))
	for i, fb := range fallbacks {
		boundFallbacks[i] = bind(fb)
	}
	return bind(mc), boundFallbacks
}

// RunTask runs a plain subagent (no named agent) with the dedicated subagent
// system prompt.
func (r *agentRunner) RunTask(ctx context.Context, task string, model string, opts tools.SubagentOptions) (string, error) {
	return r.run(ctx, task, model, providers.BuildSubagentSystemPrompt(), opts)
}

// RunTaskWithSystem runs a subagent with a named agent's custom system prompt.
func (r *agentRunner) RunTaskWithSystem(ctx context.Context, task string, model string, system string, opts tools.SubagentOptions) (string, error) {
	return r.run(ctx, task, model, system, opts)
}

// run executes one subagent turn and normalizes the outcome into a result the
// parent can act on. Any hit on the iteration cap — with or without text — is
// returned as a wrapped tools.ErrSubagentTruncated, so the tools package can
// detect it via errors.Is and surface subagentResult.Truncated /
// ToolResult.Truncated without parsing free-form suffixes.
func (r *agentRunner) run(ctx context.Context, task, model, system string, opts tools.SubagentOptions) (string, error) {
	mc, err := resolveModelClient(ctx, model)
	if err != nil {
		return "", err
	}
	// No fallback models are resolved for subagents today — a named agent's
	// fallback config is not threaded through the SubAgentRunner interface,
	// so this is always nil in production. It still goes through the same
	// wrapper as the primary client so that isolation cannot regress the
	// moment fallback support is added here (see withIsolatedPool's doc
	// comment and TestWithIsolatedPool_WrapsFallbacksWithPrimary).
	var fallbacks []connector.ModelClient
	mc, fallbacks = withIsolatedPool(mc, fallbacks)

	// Resolve the iteration cap: explicit parent override wins; otherwise the
	// (unlimited) default. Tools.ResolveMaxIter centralizes nil/0/negative
	// semantics so this logic is unit-tested in tools/.
	maxIter := tools.ResolveMaxIter(opts)

	// Create collector to capture output
	c := &collector{}
	msgs := []connector.Message{
		{
			Role:    "user",
			Content: []connector.ContentBlock{{Type: "text", Text: task}},
		},
	}

	cfg := agent.Config{
		System:        system,
		MaxRetries:    1,
		MaxIterations: maxIter,
		Debug:         false,
		Tools:         &subagentToolRunner{},
		Schema:        tools.GetSubagentToolsSchemaJSON(),
		Fallbacks:     fallbacks,
	}

	_, err = agent.Run(ctx, mc, c, &msgs, cfg)
	text := strings.TrimSpace(c.text.String())

	if errors.Is(err, agent.ErrMaxIterations) {
		if text == "" {
			// Hit the cap and produced nothing — return a hard error so
			// the parent sees a clear failure and can decide to retry,
			// split, or raise the cap. We do NOT wrap ErrSubagentTruncated
			// here, because there's no partial content to surface; the
			// parent is expected to treat this as a normal subagent
			// failure and react accordingly.
			return "", fmt.Errorf("subagent hit its %d-iteration limit without producing a final answer (likely stuck in a tool-call loop); narrow the task or split it into smaller subagent calls", maxIter)
		}
		// Partial: keep the text, annotate it, and return ErrSubagentTruncated
		// so the tools package can detect it via errors.Is and set
		// subagentResult.Truncated / ToolResult.Truncated=true.
		return text + fmt.Sprintf("\n\n[note: subagent stopped at its %d-iteration limit; the result above may be incomplete]", maxIter),
			fmt.Errorf("%w: stopped at its %d-iteration limit; result may be incomplete", tools.ErrSubagentTruncated, maxIter)
	}
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("subagent finished without producing any text output")
	}
	return text, nil
}

// subagentToolRunner wraps the global tool registry so subagents can use tools.
type subagentToolRunner struct{}

func (r *subagentToolRunner) Run(ctx context.Context, name string, args map[string]any) (string, error) {
	if name == "subagent" {
		return "", fmt.Errorf("subagent tool is not available to subagents (recursion denied)")
	}
	res := tools.RunTool(ctx, name, args)
	if res.Success {
		return res.Content, nil
	}
	return res.Content, fmt.Errorf("%s", res.Error)
}

func main() {
	// Register the subagent runner so the "subagent" tool (advertised in the
	// tool schema) is actually executable. Without this, RunTool returns
	// "unknown tool: subagent".
	tools.SetSubAgentRunner(&agentRunner{})

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
