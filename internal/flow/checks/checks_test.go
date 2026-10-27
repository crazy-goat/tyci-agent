package checks

import (
	"os"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

const issueScript = "issue_done.sh"

// ghBody answers gh from env vars: ISSUE_JSON, PERM_RESP, GH_FAIL, GH_LOGIN (the running
// account, default "me"), USER_FAIL (fail "gh api user") and EDIT_FAIL (fail "gh issue edit").
const ghBody = `
if [ -n "${GH_FAIL:-}" ]; then echo "boom" >&2; exit 1; fi
case "$*" in
  "issue view"*) echo "$ISSUE_JSON" ;;
  "api -i"*) printf '%b' "$PERM_RESP"; case "$PERM_RESP" in *" 200 "*) ;; *) exit 1 ;; esac ;;
  "api user"*) [ -n "${USER_FAIL:-}" ] && exit 1; echo "${GH_LOGIN:-me}" ;;
  "issue edit"*) [ -n "${EDIT_FAIL:-}" ] && exit 1; exit 0 ;;
  *) exit 2 ;;
esac
`

func issueJSON(state, login string, labels ...string) string {
	return issueJSONAssigned(state, login, "", labels...)
}

// issueJSONAssigned is issueJSON with one assignee login ("" for no assignee).
func issueJSONAssigned(state, login, assignee string, labels ...string) string {
	var ls []string
	for _, l := range labels {
		ls = append(ls, `{"name":"`+l+`"}`)
	}
	var as string
	if assignee != "" {
		as = `{"login":"` + assignee + `"}`
	}
	return `{"state":"` + state + `","author":{"login":"` + login + `"},"labels":[` + strings.Join(ls, ",") + `],"assignees":[` + as + `]}`
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

// An unassigned issue is assigned to the running account before the run goes on (rabbit-stream #836).
func TestIssueDone_UnassignedIsAssignedToRunAccount(t *testing.T) {
	key, exit, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write"), "GH_LOGIN": "bob"})
	if key != "go" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if !strings.Contains(log, "issue edit 7 -R o/r --add-assignee bob") {
		t.Fatalf("issue not assigned to the run account: %s", log)
	}
}

// An issue that another account works on is skipped, and it is not assigned again.
func TestIssueDone_AssignedToOtherAccountSkips(t *testing.T) {
	key, exit, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSONAssigned("OPEN", "alice", "carol", "accepted"), "PERM_RESP": perm("admin"), "GH_LOGIN": "bob"})
	if key != "skip" || exit != 0 {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	if strings.Contains(log, "issue edit") {
		t.Fatalf("issue assigned although another account has it: %s", log)
	}
}

func TestIssueDone_AssignedToRunAccountGoes(t *testing.T) {
	key, _, _, _, _ := run(t, map[string]string{"ISSUE_JSON": issueJSONAssigned("OPEN", "alice", "bob", "accepted"), "PERM_RESP": perm("write"), "GH_LOGIN": "bob"})
	if key != "go" {
		t.Fatalf("key=%q", key)
	}
}

// The assignment comes after the permission check: a reader does not get an assignee.
func TestIssueDone_ReadPermissionDoesNotAssign(t *testing.T) {
	key, _, _, _, log := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("read"), "GH_LOGIN": "bob"})
	if key != "skip" || strings.Contains(log, "issue edit") || strings.Contains(log, "api user") {
		t.Fatalf("key=%q log=%s", key, log)
	}
}

func TestIssueDone_UserLookupFailureExitsNonZero(t *testing.T) {
	key, exit, stdout, stderr, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write"), "USER_FAIL": "1"})
	if exit == 0 || key != "" || stdout != "" {
		t.Fatalf("key=%q exit=%d stdout=%q", key, exit, stdout)
	}
	wantBlock(t, stderr, "error", "could not read the authenticated GitHub account")
}

func TestIssueDone_AssignFailureExitsNonZero(t *testing.T) {
	key, exit, stdout, stderr, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm("write"), "EDIT_FAIL": "1"})
	if exit == 0 || key != "" || stdout != "" {
		t.Fatalf("key=%q exit=%d stdout=%q", key, exit, stdout)
	}
	wantBlock(t, stderr, "error", "could not assign issue #7 to 'me'")
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
	key, exit, _, stderr, _ := run(t, map[string]string{"GH_FAIL": "1"})
	if exit == 0 || key != "" {
		t.Fatalf("key=%q exit=%d", key, exit)
	}
	wantBlock(t, stderr, "error", "gh issue view")
}

func TestIssueDone_PermissionAPI500ExitsNonZero(t *testing.T) {
	key, exit, stdout, stderr, _ := run(t, map[string]string{"ISSUE_JSON": issueJSON("OPEN", "alice", "accepted"), "PERM_RESP": perm500})
	if exit == 0 || key != "" || stdout != "" {
		t.Fatalf("key=%q exit=%d stdout=%q", key, exit, stdout)
	}
	wantBlock(t, stderr, "error", "HTTP 500")
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
