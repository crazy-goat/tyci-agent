package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// setupValidate writes candidates 1 and 2, the open milestone v0.10.0 and the split answer of a run.
func setupValidate(t *testing.T, answer string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"candidates.json": `[{"number":1,"title":"first","body":""},{"number":2,"title":"second | pipe","body":""}]`,
		"milestones.json": `[{"title":"v0.10.0"}]`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	split := filepath.Join(dir, "artifacts", "001-split")
	if err := os.MkdirAll(split, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(split, "output.txt"), []byte(answer), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runValidate(t *testing.T, dir string) string {
	t.Helper()
	key, exit, _ := testutil.RunCheck(t, "rebalance_validate.sh", map[string]string{"TYCI_REPO": "o/r", "TYCI_RUN_DIR": dir})
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	return key
}

func TestRebalanceValidate_Rules(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		key    string
		reason string // a part of validate.err when key is invalid
	}{
		{"valid split", `{"assign":{"v0.10.0":[1],"v0.11.0":[2]},"create":["v0.11.0"]}`, "ok", ""},
		{"code fence is ignored", "```json\n{\"assign\":{\"v0.10.0\":[1,2]},\"create\":[]}\n```", "ok", ""},
		{"candidate missing", `{"assign":{"v0.10.0":[1]},"create":[]}`, "invalid", "not assigned"},
		{"issue twice", `{"assign":{"v0.10.0":[1,2],"v0.11.0":[2]},"create":["v0.11.0"]}`, "invalid", "more than one"},
		{"unknown issue", `{"assign":{"v0.10.0":[1,2,99]},"create":[]}`, "invalid", "not a candidate"},
		{"milestone not known", `{"assign":{"v0.10.0":[1],"v0.11.0":[2]},"create":[]}`, "invalid", "does not exist"},
		{"create exists", `{"assign":{"v0.10.0":[1,2]},"create":["v0.10.0"]}`, "invalid", "already exists"},
		{"create twice", `{"assign":{"v0.10.0":[1],"v0.11.0":[2]},"create":["v0.11.0","v0.11.0"]}`, "invalid", "twice"},
		{"bad title", `{"assign":{"v0.10.0":[1],"next":[2]},"create":["next"]}`, "invalid", "vX.Y.Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := setupValidate(t, c.answer)
			key := runValidate(t, dir)
			if key != c.key {
				t.Fatalf("key=%q, want %q", key, c.key)
			}
			errText, _ := os.ReadFile(filepath.Join(dir, "validate.err"))
			if c.key == "invalid" && !strings.Contains(string(errText), c.reason) {
				t.Fatalf("validate.err=%q, want %q", errText, c.reason)
			}
		})
	}
}

func TestRebalanceValidate_WritesProposalTable(t *testing.T) {
	dir := setupValidate(t, `{"assign":{"v0.10.0":[1],"v0.11.0":[2]},"create":["v0.11.0"]}`)
	if key := runValidate(t, dir); key != "ok" {
		t.Fatalf("key=%q", key)
	}
	table, err := os.ReadFile(filepath.Join(dir, "proposal.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| #1 | first | v0.10.0 |", `| #2 | second \| pipe | v0.11.0 (new) |`} {
		if !strings.Contains(string(table), want) {
			t.Fatalf("proposal.md = %q, want the row %q", table, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "proposal.json")); err != nil {
		t.Fatalf("proposal.json: %v", err)
	}
}

func TestRebalanceValidate_InvalidRemovesStaleProposal(t *testing.T) {
	dir := setupValidate(t, `{"assign":{"v0.10.0":[1,2]},"create":[]}`)
	if key := runValidate(t, dir); key != "ok" {
		t.Fatalf("first run key=%q", key)
	}
	if err := os.WriteFile(filepath.Join(dir, "artifacts", "001-split", "output.txt"), []byte(`{"assign":{"v0.10.0":[1]},"create":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if key := runValidate(t, dir); key != "invalid" {
		t.Fatalf("second run key=%q", key)
	}
	for _, name := range []string{"proposal.json", "proposal.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s is still there after an invalid split", name)
		}
	}
}

func TestRebalanceValidate_WrongFormFails(t *testing.T) {
	dir := setupValidate(t, `not json`)
	if key := runValidate(t, dir); key != "fail" {
		t.Fatalf("key=%q", key)
	}
}

func TestRebalanceValidate_NoAnswerFails(t *testing.T) {
	dir := t.TempDir()
	if key := runValidate(t, dir); key != "fail" {
		t.Fatalf("key=%q", key)
	}
}
