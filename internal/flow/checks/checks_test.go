package checks

import (
	"os"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

const issueScript = "issue_done.sh"

// ghBody answers gh from env vars: ISSUE_JSON, PERM_RESP, GH_FAIL.
const ghBody = `
if [ -n "${GH_FAIL:-}" ]; then echo "boom" >&2; exit 1; fi
case "$*" in
  "issue view"*) echo "$ISSUE_JSON" ;;
  "api -i"*) printf '%b' "$PERM_RESP"; case "$PERM_RESP" in *" 200 "*) ;; *) exit 1 ;; esac ;;
  *) exit 2 ;;
esac
`

func issueJSON(state, login string, labels ...string) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, `{"name":"`+l+`"}`)
	}
	return `{"state":"` + state + `","author":{"login":"` + login + `"},"labels":[` + strings.Join(ls, ",") + `]}`
}

func perm(p string) string {
	return "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n{\"permission\":\"" + p + "\"}\n"
}

const perm404 = "HTTP/2.0 404 Not Found\r\n\r\n{\"message\":\"Not Found\"}\n"
const perm500 = "HTTP/2.0 500 Internal Server Error\r\n\r\n{}\n"

func run(t *testing.T, env map[string]string) (key string, exit int, stdout, stderr, log string) {
	t.Helper()
	logPath := testutil.StubGH(t, ghBody)
	env["TYCI_REPO"] = "o/r"
	env["TYCI_ISSUE"] = "7"
	key, exit, stdout, stderr = testutil.RunCheckOut(t, issueScript, env)
	b, _ := os.ReadFile(logPath)
	return key, exit, stdout, stderr, string(b)
}

func TestIssueDone_NoLabelSkips(t *testing.T) {
	key, exit, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "bug")})
	if key != "skip" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if strings.Contains(log, "collaborators") {
		t.Fatalf("permission API called: %s", log)
	}
}

func TestIssueDone_ReadPermissionSkips(t *testing.T) {
	key, _, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("read")})
	if key != "skip" {
		t.Fatalf("key=%q", key)
	}
}

func TestIssueDone_WriteAndLabelGoes(t *testing.T) {
	key, _, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write")})
	if key != "go" {
		t.Fatalf("key=%q", key)
	}
}

func TestIssueDone_AdminGoes(t *testing.T) {
	key, _, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("admin")})
	if key != "go" {
		t.Fatalf("key=%q", key)
	}
}

func TestIssueDone_ClosedSkips(t *testing.T) {
	key, _, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("CLOSED", "alice", "accepted"), "PERM_RESP": perm("admin")})
	if key != "skip" || strings.Contains(log, "collaborators") {
		t.Fatalf("key=%q log=%s", key, log)
	}
}

func TestIssueDone_Permission404Skips(t *testing.T) {
	key, exit, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm404})
	if key != "skip" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestIssueDone_BotAuthorHandled(t *testing.T) {
	key, exit, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "app/dependabot", "accepted"), "PERM_RESP": perm404})
	if key != "skip" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if !strings.Contains(log, "collaborators/dependabot/permission") {
		t.Fatalf("app/ prefix not stripped: %s", log)
	}
}

func TestIssueDone_WeirdAuthorSkips(t *testing.T) {
	key, exit, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "x/../y", "accepted"), "PERM_RESP": perm("admin")})
	if key != "skip" || exit != 0 || strings.Contains(log, "collaborators") {
		t.Fatalf("key=%q exit=%d log=%s", key, exit, log)
	}
}

func TestIssueDone_GhFailureExitsNonZero(t *testing.T) {
	key, exit, _, _, _ := run(t, map[string]string{"GH_FAIL": "1"})
	if exit == 0 || key != "" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
}

func TestIssueDone_PermissionAPI500ExitsNonZero(t *testing.T) {
	key, exit, stdout, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm500})
	if exit == 0 || key != "" || stdout != "" {
		t.Fatalf("key=%q exit=%d stdout=%q", key, exit, stdout)
	}
}

func TestIssueDone_CustomLabelEnv(t *testing.T) {
	key, _, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "ready"), "PERM_RESP": perm("write"), "TYCI_ACCEPT_LABEL": "ready"})
	if key != "go" {
		t.Fatalf("key=%q", key)
	}
	key, _, _, _, _ = run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write"), "TYCI_ACCEPT_LABEL": "ready"})
	if key != "skip" {
		t.Fatalf("default label accepted with custom env: %q", key)
	}
}

func TestIssueDone_NeverPrintsToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "ghp_secret")
	_, _, stdout, stderr, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write"), "GH_TOKEN": "ghp_secret"})
	if strings.Contains(stdout+stderr, "ghp_secret") {
		t.Fatal("token leaked")
	}
}
