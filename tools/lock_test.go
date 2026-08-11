package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/decodo/tyci/locks"
)

func TestLockToolRunSuccess(t *testing.T) {
	lt := &LockTool{Registry: locks.NewRegistry()}

	res := lt.Run(context.Background(), map[string]any{"path": "/a/b.go"})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "holder-") {
		t.Fatalf("expected content to contain a holder id, got: %s", res.Content)
	}
	if !strings.Contains(res.Content, "session ends") {
		t.Fatalf("expected content to mention session-lifetime lock when seconds omitted, got: %s", res.Content)
	}
}

func TestLockToolRunWithSeconds(t *testing.T) {
	lt := &LockTool{Registry: locks.NewRegistry()}

	res := lt.Run(context.Background(), map[string]any{"path": "/a/b.go", "seconds": 30})
	if !res.Success {
		t.Fatalf("expected success, got error: %s", res.Error)
	}
	if !strings.Contains(res.Content, "30s") {
		t.Fatalf("expected content to mention TTL, got: %s", res.Content)
	}
}

func TestLockToolRunConflict(t *testing.T) {
	reg := locks.NewRegistry()
	lt := &LockTool{Registry: reg}

	res1 := lt.Run(context.Background(), map[string]any{"path": "/a/b.go"})
	if !res1.Success {
		t.Fatalf("expected first lock to succeed, got: %s", res1.Error)
	}

	res2 := lt.Run(context.Background(), map[string]any{"path": "/a/b.go"})
	if res2.Success {
		t.Fatal("expected second lock on same path to fail")
	}
	if !strings.Contains(res2.Error, "already locked by") {
		t.Fatalf("expected conflict error to mention holder, got: %s", res2.Error)
	}
}

func TestLockToolMissingPath(t *testing.T) {
	lt := &LockTool{Registry: locks.NewRegistry()}
	res := lt.Run(context.Background(), map[string]any{})
	if res.Success {
		t.Fatal("expected missing path to fail")
	}
}

func TestLockToolNilRegistry(t *testing.T) {
	lt := &LockTool{}
	res := lt.Run(context.Background(), map[string]any{"path": "/a"})
	if res.Success {
		t.Fatal("expected nil registry to fail")
	}
}

func TestLockToolInvalidSeconds(t *testing.T) {
	lt := &LockTool{Registry: locks.NewRegistry()}
	res := lt.Run(context.Background(), map[string]any{"path": "/a", "seconds": -5})
	if res.Success {
		t.Fatal("expected negative seconds to fail")
	}
}

func extractHolder(t *testing.T, content string) string {
	t.Helper()
	idx := strings.Index(content, "holder \"")
	if idx == -1 {
		t.Fatalf("could not find holder id in content: %s", content)
	}
	rest := content[idx+len("holder \""):]
	end := strings.Index(rest, "\"")
	if end == -1 {
		t.Fatalf("could not find end of holder id in content: %s", content)
	}
	return rest[:end]
}

func TestUnlockToolRunSuccess(t *testing.T) {
	reg := locks.NewRegistry()
	lt := &LockTool{Registry: reg}
	ut := &UnlockTool{Registry: reg}

	lockRes := lt.Run(context.Background(), map[string]any{"path": "/a/b.go"})
	if !lockRes.Success {
		t.Fatalf("expected lock to succeed, got: %s", lockRes.Error)
	}
	holder := extractHolder(t, lockRes.Content)

	unlockRes := ut.Run(context.Background(), map[string]any{"path": "/a/b.go", "holder": holder})
	if !unlockRes.Success {
		t.Fatalf("expected unlock to succeed, got: %s", unlockRes.Error)
	}

	if l, locked := reg.IsLocked("/a/b.go"); locked {
		t.Fatalf("expected path to be unlocked, got %+v", l)
	}
}

func TestUnlockToolWrongHolder(t *testing.T) {
	reg := locks.NewRegistry()
	lt := &LockTool{Registry: reg}
	ut := &UnlockTool{Registry: reg}

	lockRes := lt.Run(context.Background(), map[string]any{"path": "/a/b.go"})
	if !lockRes.Success {
		t.Fatalf("expected lock to succeed, got: %s", lockRes.Error)
	}

	unlockRes := ut.Run(context.Background(), map[string]any{"path": "/a/b.go", "holder": "someone-else"})
	if unlockRes.Success {
		t.Fatal("expected unlock with wrong holder to fail")
	}

	if _, locked := reg.IsLocked("/a/b.go"); !locked {
		t.Fatal("expected path to remain locked")
	}
}

func TestUnlockToolUnknownPath(t *testing.T) {
	ut := &UnlockTool{Registry: locks.NewRegistry()}
	res := ut.Run(context.Background(), map[string]any{"path": "/nope", "holder": "x"})
	if res.Success {
		t.Fatal("expected unlock of unknown path to fail")
	}
}

func TestUnlockToolMissingFields(t *testing.T) {
	ut := &UnlockTool{Registry: locks.NewRegistry()}

	if res := ut.Run(context.Background(), map[string]any{"holder": "x"}); res.Success {
		t.Fatal("expected missing path to fail")
	}
	if res := ut.Run(context.Background(), map[string]any{"path": "/a"}); res.Success {
		t.Fatal("expected missing holder to fail")
	}
}

func TestUnlockToolNilRegistry(t *testing.T) {
	ut := &UnlockTool{}
	res := ut.Run(context.Background(), map[string]any{"path": "/a", "holder": "x"})
	if res.Success {
		t.Fatal("expected nil registry to fail")
	}
}

func TestLockUnlockRegisteredInToolRegistry(t *testing.T) {
	lockTool, ok := toolRegistry["lock"]
	if !ok {
		t.Fatal("expected \"lock\" tool to be registered")
	}
	if _, ok := lockTool.(*LockTool); !ok {
		t.Fatal("expected \"lock\" tool to be a *LockTool")
	}
	unlockTool, ok := toolRegistry["unlock"]
	if !ok {
		t.Fatal("expected \"unlock\" tool to be registered")
	}
	if _, ok := unlockTool.(*UnlockTool); !ok {
		t.Fatal("expected \"unlock\" tool to be a *UnlockTool")
	}
}
