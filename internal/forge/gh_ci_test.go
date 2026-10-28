//go:build unix

package forge_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ciJSON = `[
{"databaseId":9,"conclusion":"","status":"in_progress","headSha":"s9","createdAt":"2026-10-01T10:09:00Z"},
{"databaseId":8,"conclusion":"cancelled","status":"completed","headSha":"s8","createdAt":"2026-10-01T10:08:00Z"},
{"databaseId":7,"conclusion":"failure","status":"completed","headSha":"s7","createdAt":"2026-10-01T10:07:00Z"},
{"databaseId":6,"conclusion":"success","status":"completed","headSha":"s6","createdAt":"2026-10-01T10:06:00Z"}]`

func TestGitHub_LatestCISkipsRunningAndCancelled(t *testing.T) {
	g, _ := newStub(t, route{match: "databaseId,conclusion,status,headSha,createdAt", stdout: ciJSON})
	r, ok, err := g.LatestCI(context.Background(), "tests.yaml", "main")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if r.ID != 7 || r.SHA != "s7" || !r.Failed || r.CreatedAt.Minute() != 7 {
		t.Fatalf("run %+v", r)
	}
}

func TestGitHub_LatestCIGreenAndEmpty(t *testing.T) {
	g, _ := newStub(t, route{match: "green", stdout: `[{"databaseId":1,"conclusion":"success","status":"completed","headSha":"a","createdAt":"2026-10-01T10:00:00Z"}]`},
		route{match: "none", stdout: `[]`})
	r, ok, err := g.LatestCI(context.Background(), "tests.yaml", "green")
	if err != nil || !ok || r.Failed {
		t.Fatalf("green: %+v ok=%v err=%v", r, ok, err)
	}
	if _, ok, err := g.LatestCI(context.Background(), "tests.yaml", "none"); err != nil || ok {
		t.Fatalf("none: ok=%v err=%v", ok, err)
	}
}

func TestGitHub_LatestCIError(t *testing.T) {
	g, _ := newStub(t, route{match: "headSha", stderr: "HTTP 502", exit: 1})
	if _, _, err := g.LatestCI(context.Background(), "tests.yaml", "main"); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err %v", err)
	}
}

func TestGitHub_CommentPostsBodyAsOneArgument(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "gh")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + args + "'; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g, _ := newStub(t)
	g.SetBin(bin)
	if err := g.Comment(context.Background(), 12, "run x waits; $(rm -rf /)"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	want := "api\n-X\nPOST\nrepos/o/r/issues/12/comments\n-f\nbody=run x waits; $(rm -rf /)\n"
	if string(b) != want {
		t.Fatalf("args:\n%s", b)
	}
	if err := g.Comment(context.Background(), 0, "x"); err == nil {
		t.Fatal("issue 0 must fail")
	}
}
