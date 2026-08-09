package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/internal/debug"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/tools"
)

// TestAgentRunnerRun_TemperatureReachesRequest is the end of the temperature
// path from a markdown agent definition to the wire: tools/subagent.go's
// runSingleTask already puts def.Temperature into SubagentOptions.Temperature
// (see tools/subagent_test.go), and agentRunner.run must forward it into
// agent.Config.Temperature, which agent/run_once.go puts on every
// connector.Request. connectortest.Fake records the requests it is handed, so
// this is checked directly against the model double, with no real provider.
func TestAgentRunnerRun_TemperatureReachesRequest(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	temp := 0.9
	opts := tools.SubagentOptions{Temperature: &temp}

	r := &agentRunner{}
	got, err := r.run(ctx, "do the thing", "", "be helpful", opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "child answer" {
		t.Fatalf("run() = %q, want %q", got, "child answer")
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Temperature == nil || *reqs[0].Temperature != 0.9 {
		t.Fatalf("Request.Temperature = %v, want pointer to 0.9", reqs[0].Temperature)
	}
}

// A nil SubagentOptions.Temperature (the common case: no `temperature` line
// in the agent's frontmatter) must leave the wire request untouched — no
// silent default of 0.
func TestAgentRunnerRun_NoTemperatureLeavesRequestNil(t *testing.T) {
	fake := connectortest.Text("child answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	r := &agentRunner{}
	_, err := r.run(ctx, "do the thing", "", "be helpful", tools.SubagentOptions{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	if reqs[0].Temperature != nil {
		t.Errorf("Request.Temperature = %v, want nil", *reqs[0].Temperature)
	}
}

// fixedClientProvider is a providers.Provider whose Client always returns the
// same pre-built connector.ModelClient, so a test can hand agentRunner.run a
// fallback spec that resolves to a specific connectortest.Fake it can later
// inspect — fakeProvider (main_resolve_test.go) always mints a fresh Fake per
// call, which would make the returned client unobservable here.
type fixedClientProvider struct {
	name   string
	client connector.ModelClient
}

func (f *fixedClientProvider) Name() string             { return f.name }
func (f *fixedClientProvider) IsConfigured() bool       { return true }
func (f *fixedClientProvider) Models() []string         { return nil }
func (f *fixedClientProvider) ConfigWarnings() []string { return nil }
func (f *fixedClientProvider) Client(string) connector.ModelClient {
	return f.client
}

// TestAgentRunnerRun_FallbacksResolveAndCarryTemperature exercises the whole
// chain for a subagent's fallback list: opts.Fallbacks ("provider/model"
// specs from the named agent's frontmatter) must be resolved and handed to
// agent.Config.Fallbacks, and — since agent.Run passes the SAME cfg to every
// fallback attempt — cfg.Temperature must reach the fallback's request too
// once the primary fails.
func TestAgentRunnerRun_FallbacksResolveAndCarryTemperature(t *testing.T) {
	fbFake := connectortest.Text("fallback answer")
	providers.Register(&fixedClientProvider{name: "child-fb-prov", client: fbFake})

	primary := &connectortest.Fake{ProviderName: "child-primary-prov", ModelName: "primary-model", StreamErr: errors.New("primary down")}
	ctx := connector.WithModelClient(context.Background(), primary)

	temp := 0.5
	opts := tools.SubagentOptions{
		Temperature: &temp,
		Fallbacks:   []string{"child-fb-prov/fb-model"},
	}

	r := &agentRunner{}
	got, err := r.run(ctx, "do the thing", "", "be helpful", opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got != "fallback answer" {
		t.Fatalf("run() = %q, want %q", got, "fallback answer")
	}

	reqs := fbFake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request to the fallback, got %d", len(reqs))
	}
	if reqs[0].Temperature == nil || *reqs[0].Temperature != 0.5 {
		t.Fatalf("fallback Request.Temperature = %v, want pointer to 0.5", reqs[0].Temperature)
	}
}

// TestAgentRunnerRun_UnresolvedFallbackLogsToDebugNotStderr is the behavior
// this refactor exists for: a subagent runs mid-session, often under the
// Bubble Tea TUI, so an unresolved fallback spec must never touch os.Stderr
// (unlike the top-level agent's resolveFallbacks) — and it must not fail the
// whole call either, since a fallback is best-effort and the primary is
// perfectly capable of answering on its own.
func TestAgentRunnerRun_UnresolvedFallbackLogsToDebugNotStderr(t *testing.T) {
	fake := connectortest.Text("primary answer")
	ctx := connector.WithModelClient(context.Background(), fake)

	// A real debug.Logger writing into a hermetic HOME, so the assertion
	// reads back exactly what production code (debug.FromContext(ctx).Write)
	// would have written — no substitute writer that could drift from the
	// real Logger's behavior.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	dl, err := debug.Init()
	if err != nil {
		t.Fatalf("debug.Init: %v", err)
	}
	t.Cleanup(dl.Close)
	ctx = debug.NewContext(ctx, dl)

	opts := tools.SubagentOptions{Fallbacks: []string{"totally-unregistered-prov/ghost"}}

	stderr := captureStderr(t, func() {
		r := &agentRunner{}
		got, err := r.run(ctx, "do the thing", "", "be helpful", opts)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if got != "primary answer" {
			t.Fatalf("run() = %q, want the primary's answer despite the unresolved fallback", got)
		}
	})
	if stderr != "" {
		t.Errorf("unresolved fallback spec leaked to stderr: %q", stderr)
	}

	dl.Close() // flush before reading the file back
	data, err := os.ReadFile(filepath.Join(dir, ".tyci", "debug", dl.ID+".log"))
	if err != nil {
		t.Fatalf("read debug log: %v", err)
	}
	if !strings.Contains(string(data), "totally-unregistered-prov/ghost") {
		t.Errorf("expected the unresolved spec to be logged to the debug log, got %q", string(data))
	}
}
