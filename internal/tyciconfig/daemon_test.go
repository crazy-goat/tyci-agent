package tyciconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDaemonDefaults(t *testing.T) {
	d, err := LoadDaemon(filepath.Join(t.TempDir(), "missing.json"), "")
	if err != nil {
		t.Fatal(err)
	}
	want := Daemon{Enabled: true, PollInterval: 60 * time.Second, Workers: 3, Issue: true, CIFailure: true, Cron: true}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got %+v, want %+v", d, want)
	}
}

func TestLoadDaemonProjectWinsPerKey(t *testing.T) {
	user := writeConfig(t, `{"daemon":{"poll_interval":"30s","workers":5,"triggers":{"issue":false,"cron":false},"repos":["a/b"]}}`)
	project := writeConfig(t, `{"daemon":{"workers":0,"triggers":{"cron":true}}}`)
	d, err := LoadDaemon(user, project)
	if err != nil {
		t.Fatal(err)
	}
	want := Daemon{Enabled: true, PollInterval: 30 * time.Second, Workers: 0, Issue: false, CIFailure: true, Cron: true, Repos: []string{"a/b"}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("got %+v, want %+v", d, want)
	}
}

func TestLoadDaemonRejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"daemon":{"poll_interval":"4s"}}`, "daemon.poll_interval"},
		{`{"daemon":{"poll_interval":"soon"}}`, "daemon.poll_interval"},
		{`{"daemon":{"workers":-1}}`, "daemon.workers"},
		{`{"daemon":{"workers":"3"}}`, "daemon.workers"},
		{`{"daemon":{"repos":["nope"]}}`, "daemon.repos"},
		{`{"daemon":{"repos":["a/b/c"]}}`, "daemon.repos"},
	} {
		p := writeConfig(t, tc.body)
		if _, err := LoadDaemon(p, ""); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: got %v, want an error about %s that names the file", tc.body, err, tc.want)
		}
	}
}

func TestLoadDaemonNamesTheProjectFileForABadProjectKey(t *testing.T) {
	user := writeConfig(t, `{"daemon":{"workers":2}}`)
	project := writeConfig(t, `{"daemon":{"workers":-2}}`)
	if _, err := LoadDaemon(user, project); err == nil || !strings.Contains(err.Error(), project) {
		t.Fatalf("got %v, want an error that names %s", err, project)
	}
}
