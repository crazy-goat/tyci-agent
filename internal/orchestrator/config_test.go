package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOrchestratorConfigDefaults(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none.json")
	for _, p := range []string{missing, writeCfg(t, `{}`), writeCfg(t, `{"orchestrator":{}}`)} {
		c, err := LoadConfig(p, missing)
		if err != nil || c.Workers != 3 || c.PlanTimeout != 0 || c.AcceptedLabel != "accepted" {
			t.Fatalf("%s: %+v %v", p, c, err)
		}
	}
}

func TestOrchestratorConfigOverride(t *testing.T) {
	u := writeCfg(t, `{"orchestrator":{"workers":5,"accepted_label":"ok"}}`)
	p := writeCfg(t, `{"orchestrator":{"workers":2,"plan_timeout_sec":30}}`)
	c, err := LoadConfig(u, p)
	if err != nil || c.Workers != 2 || c.AcceptedLabel != "ok" || c.PlanTimeout != 30*time.Second {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestOrchestratorConfigZeroIsKept(t *testing.T) {
	c, err := LoadConfig(writeCfg(t, `{"orchestrator":{"workers":0}}`), "")
	if err != nil || c.Workers != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestOrchestratorConfigInvalid(t *testing.T) {
	cases := []struct{ body, key string }{
		{`{"orchestrator":{"workers":-1}}`, "orchestrator.workers"},
		{`{"orchestrator":{"workers":"3"}}`, "orchestrator.workers"},
		{`{"orchestrator":{"plan_timeout_sec":-5}}`, "orchestrator.plan_timeout_sec"},
		{`{"orchestrator":{"plan_timeout_sec":"0"}}`, "orchestrator.plan_timeout_sec"},
		{`{"orchestrator":{"accepted_label":""}}`, "orchestrator.accepted_label"},
		{`{"orchestrator":{"accepted_label":5}}`, "orchestrator.accepted_label"},
	}
	for _, tc := range cases {
		for _, asProject := range []bool{false, true} {
			p := writeCfg(t, tc.body)
			u, pr := p, ""
			if asProject {
				u, pr = "", p
			}
			_, err := LoadConfig(u, pr)
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), p) {
				t.Fatalf("%s project=%v: %v", tc.body, asProject, err)
			}
		}
	}
}

func TestAcquireRelease(t *testing.T) {
	o := New(Config{Workers: 3}, nil, nil, Hooks{})
	if !o.acquire(1) || o.acquire(1) {
		t.Fatal("second acquire must fail")
	}
	o.release(1)
	if !o.acquire(1) {
		t.Fatal("acquire after release must work")
	}
}

func TestAcquireRace(t *testing.T) {
	o := New(Config{Workers: 0}, nil, nil, Hooks{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o.acquire(7) {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("wins %d", wins)
	}
}

func TestAcquireRespectsLimit(t *testing.T) {
	o := New(Config{Workers: 2}, nil, nil, Hooks{})
	if !o.acquire(1) || !o.acquire(2) || o.acquire(3) || o.busy() != 2 {
		t.Fatal("limit not respected")
	}
}
