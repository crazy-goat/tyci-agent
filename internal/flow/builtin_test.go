package flow

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flowconfig"
)

func builtinWF(t *testing.T) *Workflow {
	t.Helper()
	wf, src, err := Lookup("issue-to-merge", t.TempDir(), "", false)
	if err != nil || src != "builtin" {
		t.Fatalf("Lookup: %v %q", err, src)
	}
	return wf
}

func embeddedResolver(t *testing.T) Resolver {
	runDir := t.TempDir()
	return func(rel string) (string, error) { return ResolveCheck(rel, "", "", Embedded(), runDir) }
}

func TestBuiltin_ParsesAndValidates(t *testing.T) {
	wf := builtinWF(t)
	cfg := func(w, r string) *flowconfig.Config {
		return &flowconfig.Config{
			Models: map[string]string{"a": "x://" + w, "b": "x://" + r},
			Roles:  map[string]flowconfig.Role{"worker": {Model: "a"}, "review": {Model: "b"}, "merge_decision": {Model: "a"}},
		}
	}
	warn, err := Validate(wf, cfg("1", "2"), embeddedResolver(t))
	if err != nil || len(warn) != 0 {
		t.Fatalf("diff models: warnings=%v err=%v", warn, err)
	}
	warn, err = Validate(wf, cfg("1", "1"), embeddedResolver(t))
	if err != nil || len(warn) != 1 {
		t.Fatalf("same model: warnings=%v err=%v", warn, err)
	}
}

func TestBuiltin_MaxVisitsOnCodeAndCI(t *testing.T) {
	wf := builtinWF(t)
	if wf.States["code"].MaxVisits != 3 || wf.States["ci"].MaxVisits != 3 || wf.States["ci"].TimeoutSec != 5400 {
		t.Fatalf("bad limits: %+v %+v", wf.States["code"], wf.States["ci"])
	}
}

func TestBuiltin_AllEmbeddedScriptsExist(t *testing.T) {
	for name, s := range builtinWF(t).States {
		if s.Check == "" {
			continue
		}
		if _, err := fs.Stat(Embedded(), s.Check); err != nil {
			t.Errorf("state %s: %v", name, err)
		}
	}
}

func writeWF(t *testing.T, base, name string) {
	t.Helper()
	dir := filepath.Join(base, ".tyci", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"name":"` + filepath.Base(base) + `","start":"end","states":{"end":{"end":true}}}`
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLookup_ProjectBeatsGlobalBeatsBuiltin(t *testing.T) {
	home, proj := filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "proj")
	_, src, _ := Lookup("issue-to-merge", home, proj, true)
	if src != "builtin" {
		t.Fatalf("source = %q", src)
	}
	writeWF(t, home, "issue-to-merge")
	wf, src, err := Lookup("issue-to-merge", home, proj, true)
	if err != nil || wf.Name != "home" || src == "builtin" {
		t.Fatalf("global: %v %q", err, src)
	}
	writeWF(t, proj, "issue-to-merge")
	wf, _, err = Lookup("issue-to-merge", home, proj, true)
	if err != nil || wf.Name != "proj" {
		t.Fatalf("project: %v", err)
	}
}

func TestLookup_UntrustedProjectIgnored(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	writeWF(t, proj, "issue-to-merge")
	_, src, err := Lookup("issue-to-merge", t.TempDir(), proj, false)
	if err != nil || src != "builtin" {
		t.Fatalf("got %q %v", src, err)
	}
}

func TestLookup_BadNameRejected(t *testing.T) {
	for _, n := range []string{"../x", "A b", ""} {
		if _, _, err := Lookup(n, "", "", true); err == nil {
			t.Errorf("name %q accepted", n)
		}
	}
}

func TestBuiltin_RunsToEndWithFakes(t *testing.T) {
	wf := builtinWF(t)
	r := &Runner{WF: wf, Store: &memStore{},
		Checks: &fakeChecks{keys: map[string][]string{
			"checks/issue_done.sh": {"go"}, "checks/push.sh": {"ok"}, "checks/post_review.sh": {"ok"}, "checks/fetch_comments.sh": {"none"},
			"checks/ci_wait.sh": {"green"}, "checks/merge.sh": {"merged"},
		}},
		Agents: &fakeAgents{keys: map[string][]string{
			"code": {"done"}, "review": {"ACCEPT"}, "findings": {"done"},
		}}}
	st := newRun("check_done")
	if err := r.Run(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "done" {
		t.Fatalf("status = %q", st.Status)
	}
}
