package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/internal/ledger"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
	"github.com/spf13/cobra"
)

// wfLedgerFakeProvider is a providers.Provider whose Client always returns a
// preset connector.ModelClient — the same shape internal/workflow's own
// fakeProvider (engine_tools_test.go) uses, duplicated here rather than
// shared across packages since providers.Provider is trivial to implement
// and this file has no other dependency on internal/workflow's test code.
type wfLedgerFakeProvider struct {
	name   string
	model  string
	client connector.ModelClient
}

func (p *wfLedgerFakeProvider) Name() string                        { return p.name }
func (p *wfLedgerFakeProvider) IsConfigured() bool                  { return true }
func (p *wfLedgerFakeProvider) Models() []string                    { return []string{p.model} }
func (p *wfLedgerFakeProvider) Client(string) connector.ModelClient { return p.client }
func (p *wfLedgerFakeProvider) ConfigWarnings() []string            { return nil }

// writeLedgerTestHome points HOME at a fresh temp dir with a stub
// providers.json, so registerProviders() (called by workflowRunCmd.RunE)
// does not try to hit models.dev's network catalog.
func writeLedgerTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tyci := filepath.Join(home, ".tyci")
	if err := os.MkdirAll(tyci, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tyci, "providers.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write providers.json: %v", err)
	}
}

// newWorkflowRunTestCmd builds a bare *cobra.Command carrying exactly the
// flag workflowRunCmd.RunE reads directly (--no-mcp; --prompt/--dir are read
// off the workflowPrompt/workflowDir package globals instead, set directly
// by the test below), with stdout/stderr captured separately so a test can
// tell which stream something landed on.
func newWorkflowRunTestCmd(t *testing.T) (cmd *cobra.Command, stdout, stderr *bytes.Buffer) {
	t.Helper()
	cmd = &cobra.Command{Use: "test"}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("no-mcp", true, "")
	stdout = &bytes.Buffer{}
	stderr = &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	prevPrompt, prevDir := workflowPrompt, workflowDir
	t.Cleanup(func() { workflowPrompt, workflowDir = prevPrompt, prevDir })
	workflowPrompt = ""
	workflowDir = ""
	return cmd, stdout, stderr
}

// TestWorkflowRunCLI_LedgerSummary_OnStderrOnly_StdoutOnlyScriptResult is
// F32's end-to-end regression test, run in-process (not through binPath)
// so a fake connector.ModelClient can drive a real session:await() without
// any network access: a workflow script's named-agent session usage must
// (1) reach the ledger (internal/ledger, via engine.go's ledger.Watch wrap
// — see internal/workflow's own TestSessionAwait_RecordsLedgerUsage for
// that half in isolation) and (2) be surfaced, since `tyci workflow run` is
// a one-shot CLI process with no TUI ever reading the ledger back — as a
// short summary on STDERR, leaving stdout carrying only the script's own
// printed result (stdout may be piped/parsed by a caller, so it must never
// be disturbed by this).
func TestWorkflowRunCLI_LedgerSummary_OnStderrOnly_StdoutOnlyScriptResult(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)
	writeLedgerTestHome(t)

	fake := &connectortest.Fake{
		ProviderName: "wf-ledger-cli-fake",
		ModelName:    "wf-ledger-cli-model",
		Turns: [][]stream.Event{
			{
				stream.TextDelta{Text: "done"},
				stream.Finish{Usage: stream.Usage{Input: 9, Output: 4}},
			},
		},
	}
	providers.Register(&wfLedgerFakeProvider{name: "wf-ledger-cli-provider", model: "wf-ledger-cli-model", client: fake})

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "summary.lua")
	script := `
		local s = tyci.new_session("wf-ledger-cli-provider/wf-ledger-cli-model")
		local reply, err = s:await()
		if err then
			return "ERR:" .. err
		end
		return "OK:" .. (reply.content or "")
	`
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	cmd, stdout, stderr := newWorkflowRunTestCmd(t)
	if err := workflowRunCmd.RunE(cmd, []string{scriptPath}); err != nil {
		t.Fatalf("workflowRunCmd.RunE: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "OK:done" {
		t.Fatalf("stdout = %q, want exactly %q (the script's own result, nothing else)", got, "OK:done")
	}
	if strings.Contains(stdout.String(), "tyci workflow:") {
		t.Fatalf("the ledger summary must not appear on stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "tyci workflow:") {
		t.Fatalf("stderr = %q, want it to contain the ledger summary (\"tyci workflow: ...\")", stderr.String())
	}
	// 9+4 tokens from the one recorded model call.
	if !strings.Contains(stderr.String(), "13 tokens") {
		t.Errorf("stderr = %q, want it to report 13 tokens (9 input + 4 output)", stderr.String())
	}
}

// TestWorkflowRunCLI_LedgerSummary_PrintedEvenOnScriptError is round 1
// finding 3's regression test: printWorkflowLedgerSummary used to run only
// on the success path (after engine.Run returned a nil error), so a script
// that drove one or more session:await() calls — spending real money,
// already recorded in the ledger — and then hit a Lua error partway through
// reported nothing at all: exactly the one moment the user most wants the
// number, and the failure mode F32 exists to prevent. RunE must defer the
// summary print unconditionally, right after setupProjectLocalEnv, not call
// it only just before its own final `return nil`.
func TestWorkflowRunCLI_LedgerSummary_PrintedEvenOnScriptError(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)
	writeLedgerTestHome(t)

	fake := &connectortest.Fake{
		ProviderName: "wf-ledger-cli-err-fake",
		ModelName:    "wf-ledger-cli-err-model",
		Turns: [][]stream.Event{
			{
				stream.TextDelta{Text: "partial"},
				stream.Finish{Usage: stream.Usage{Input: 6, Output: 1}},
			},
		},
	}
	providers.Register(&wfLedgerFakeProvider{name: "wf-ledger-cli-err-provider", model: "wf-ledger-cli-err-model", client: fake})

	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "summary_err.lua")
	// Awaits successfully (recording usage in the ledger), then fails with
	// a plain Lua runtime error — engine.Run must return a non-nil error
	// from this, while the ledger already holds what the await spent.
	script := `
		local s = tyci.new_session("wf-ledger-cli-err-provider/wf-ledger-cli-err-model")
		local reply, err = s:await()
		if err then
			error(err)
		end
		error("deliberate failure after a successful session")
	`
	if err := os.WriteFile(scriptPath, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}

	cmd, stdout, stderr := newWorkflowRunTestCmd(t)
	runErr := workflowRunCmd.RunE(cmd, []string{scriptPath})
	if runErr == nil {
		t.Fatal("expected the script's deliberate Lua error to surface as RunE's error")
	}
	if got := stdout.String(); got != "" {
		t.Errorf("stdout = %q, want nothing printed for a script that never returned a result", got)
	}
	if !strings.Contains(stderr.String(), "tyci workflow:") {
		t.Fatalf("stderr = %q, want the ledger summary even though the script errored out after its session:await()", stderr.String())
	}
	if !strings.Contains(stderr.String(), "7 tokens") {
		t.Errorf("stderr = %q, want it to report 7 tokens (6 input + 1 output) from the session that ran before the error", stderr.String())
	}
}

// TestPrintWorkflowLedgerSummary_EmptySnapshotPrintsNothing is the
// companion unit test: a script that never runs an agent session (never
// calls tyci.new_session/resume_session, or never awaits one) leaves the
// ledger empty, and printWorkflowLedgerSummary must print nothing for that
// — a "$0.00" line would be noise, not information.
func TestPrintWorkflowLedgerSummary_EmptySnapshotPrintsNothing(t *testing.T) {
	ledger.Reset()
	t.Cleanup(ledger.Reset)

	var buf bytes.Buffer
	printWorkflowLedgerSummary(&buf)
	if buf.Len() != 0 {
		t.Fatalf("printWorkflowLedgerSummary on an empty ledger wrote %q, want nothing", buf.String())
	}
}
