package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow/internal/testutil"
)

// closeGh answers the milestone list from MS_JSON (the raw list, jq filters it in the script).
// Every other call is logged; the PATCH call succeeds.
const closeGh = `
case "$*" in
  "api --paginate repos/o/r/milestones"*) printf '%s' "$MS_JSON" ;;
  "api -X PATCH repos/o/r/milestones/"*) ;;
  *) exit 2 ;;
esac
`

func TestMilestoneClose(t *testing.T) {
	cases := []struct {
		name    string
		ms      string
		key     string
		patched bool
	}{
		{"open milestone is closed", `[{"number":3,"title":"v0.10.0","state":"open"}]`, "ok", true},
		{"closed milestone is left alone", `[{"number":3,"title":"v0.10.0","state":"closed"}]`, "ok", false},
		{"unknown title fails", `[{"number":3,"title":"v0.9.0","state":"open"}]`, "fail", false},
	}
	script, _ := filepath.Abs("milestone_close.sh")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logPath := testutil.StubGH(t, closeGh)
			env := map[string]string{"TYCI_REPO": "o/r", "TYCI_PARAM_MILESTONE": "v0.10.0", "MS_JSON": tc.ms}
			key, exit, _, stderr := testutil.RunCheckIn(t, "", script, env)
			if exit != 0 || key != tc.key {
				t.Fatalf("key=%q exit=%d stderr=%s, want %q", key, exit, stderr, tc.key)
			}
			b, _ := os.ReadFile(logPath)
			if got := strings.Contains(string(b), "-X PATCH repos/o/r/milestones/3 -f state=closed"); got != tc.patched {
				t.Fatalf("PATCH sent=%v, want %v; log=%s", got, tc.patched, b)
			}
		})
	}
}
