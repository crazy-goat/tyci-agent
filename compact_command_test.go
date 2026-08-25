package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector/connectortest"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

// TestManualCompactSummary_CarriesRealDumpPathIntoConversation pins the fix
// for a human-typed /compact: the terminal used to print the dump path while
// the conversation itself only ever got a placeholder summary with no path
// in it, so the model had no way to ever find the record again on a later
// turn. interactive.go and tui_mode.go both now compute the real path via
// session.DumpPathFor before calling Compact and fold it into the summary
// that becomes the compacted history's lead message — this test exercises
// exactly that sequence, the way both call sites do it, and checks the path
// that lands in the live conversation matches the one Compact actually wrote.
func TestManualCompactSummary_CarriesRealDumpPathIntoConversation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	client := &connectortest.Fake{ProviderName: "p", ModelName: "m",
		Turns: [][]stream.Event{{stream.TextDelta{Text: "ok"}, stream.Finish{}}}}
	cond := conductor.New(conductor.Options{
		Client:      client,
		Sink:        noopDisplay{},
		SessionPath: path,
		WorkDir:     dir,
	})

	for i := 0; i < 3; i++ {
		if _, err := cond.Submit(context.Background(), "message"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}

	// Mirrors interactive.go/tui_mode.go's /compact handling exactly.
	sess := cond.EnsureSession()
	if sess == nil {
		t.Fatal("expected a writable session")
	}
	predictedPath := session.DumpPathFor(cond.SessionPath())
	returnedPath, err := cond.Compact(manualCompactSummary(predictedPath), "keep the deploy details")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if predictedPath != returnedPath {
		t.Fatalf("predicted dump path %q != path Compact actually returned %q", predictedPath, returnedPath)
	}
	if _, err := os.Stat(returnedPath); err != nil {
		t.Fatalf("dump file missing at the path folded into the summary: %v", err)
	}

	msgs := cond.Messages()
	if len(msgs) == 0 {
		t.Fatal("expected a compacted lead message")
	}
	lead := msgs[0]
	if lead.Role != "user" || len(lead.Content) == 0 {
		t.Fatalf("unexpected compacted lead message: %+v", lead)
	}
	if !strings.Contains(lead.Content[0].Text, returnedPath) {
		t.Fatalf("compacted summary does not carry the real dump path: %q", lead.Content[0].Text)
	}
	if !strings.Contains(lead.Content[0].Text, "keep the deploy details") {
		t.Fatalf("focus instruction lost from the compacted summary: %q", lead.Content[0].Text)
	}
}
