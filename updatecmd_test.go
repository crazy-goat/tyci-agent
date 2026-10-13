package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"newer", "v0.9.0", "v0.8.0", 1},
		{"older", "v0.8.0", "v0.9.0", -1},
		{"same", "v0.9.0", "v0.9.0", 0},
		{"same without v prefix", "0.9.0", "v0.9.0", 0},
		{"numeric, not lexical", "v0.10.0", "v0.9.0", 1},
		{"patch", "v1.2.4", "v1.2.3", 1},
		{"pre-release is older than release", "v0.9.0-rc.1", "v0.9.0", -1},
		{"release is newer than pre-release", "v0.9.0", "v0.9.0-rc.1", 1},
		{"pre-release order", "v0.9.0-rc.2", "v0.9.0-rc.10", -1},
		{"numeric before alphanumeric", "v1.0.0-1", "v1.0.0-alpha", -1},
		{"longer pre-release is newer", "v1.0.0-rc.1.1", "v1.0.0-rc.1", 1},
		{"dev build is not a version", "dev", "v0.9.0", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareVersions(tt.a, tt.b); got != tt.want {
				t.Fatalf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsDevBuild(t *testing.T) {
	tests := map[string]bool{
		"dev":                                  true,
		"ebb8788":                              true,
		"v0.6.0-22-gebb8788":                   true,
		"v0.6.0-22-gebb8788-dirty":             true,
		"v0.8.1-0.20261013063645-042f32f0bb8f": true,
		"v0.8.1-0.20261013063645-042f32f0bb8f+dirty": true,
		"v0.9.0-dirty": true,
		"v0.9.0":       false,
		"v0.9.0-rc.1":  false,
	}
	for v, want := range tests {
		if got := isDevBuild(v); got != want {
			t.Errorf("isDevBuild(%q) = %v, want %v", v, got, want)
		}
	}
}

// platformAsset is the asset name this test run needs.
var platformAsset = "tyci_" + runtime.GOOS + "_" + runtime.GOARCH

// useFakeRelease starts an httptest.Server that serves a release with the
// given tag. assets maps asset names to their content. checksums.txt is
// computed from assets unless sums is not empty. It points updateAPIURL at
// the server and clears the token variables, so no real network or token is used.
func useFakeRelease(t *testing.T, tag string, assets map[string][]byte, sums string) *atomic.Int32 {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")

	if sums == "" {
		names := make([]string, 0, len(assets))
		for name := range assets {
			names = append(names, name)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, name := range names {
			sum := sha256.Sum256(assets[name])
			fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		}
		sums = b.String()
	}
	assets[updateChecksumsName] = []byte(sums)

	downloads := &atomic.Int32{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == updateRepoPath+"/latest", r.URL.Path == updateRepoPath+"/tags/"+tag:
			rel := ghRelease{TagName: tag, HTMLURL: "https://example.test/release/" + tag}
			for name := range assets {
				rel.Assets = append(rel.Assets, ghAsset{Name: name, URL: srv.URL + "/dl/" + name})
			}
			_ = json.NewEncoder(w).Encode(rel)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			downloads.Add(1)
			content, ok := assets[strings.TrimPrefix(r.URL.Path, "/dl/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	old := updateAPIURL
	updateAPIURL = srv.URL
	t.Cleanup(func() { updateAPIURL = old })
	return downloads
}

// installDir returns a directory with an old "tyci" binary and its path.
func installDir(t *testing.T) (dir, exe string) {
	t.Helper()
	dir = t.TempDir()
	exe = filepath.Join(dir, "tyci")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, exe
}

func requireContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

// requireOnly fails unless dir contains exactly the named entries.
func requireOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(names, ",") {
		t.Fatalf("dir entries = %v, want %v", got, names)
	}
}

func TestRunUpdateReplacesBinary(t *testing.T) {
	tests := map[string]string{"latest": "", "explicit tag": "v0.9.0"}
	for name, tag := range tests {
		t.Run(name, func(t *testing.T) {
			useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, "")
			dir, exe := installDir(t)

			var out bytes.Buffer
			err := runUpdate(context.Background(), &out, updateOptions{version: tag}, "v0.8.0", exe)
			if err != nil {
				t.Fatalf("runUpdate: %v", err)
			}
			requireContent(t, exe, "new binary")
			info, err := os.Stat(exe)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o755 {
				t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
			}
			requireOnly(t, dir, "tyci")
			if !strings.Contains(out.String(), "updated v0.8.0 -> v0.9.0") {
				t.Fatalf("output = %q, want the update line", out.String())
			}
		})
	}
}

func TestRunUpdateRejectsBadChecksum(t *testing.T) {
	wrong := strings.Repeat("0", 64) + "  " + platformAsset + "\n"
	tests := map[string]string{
		"wrong checksum":        wrong,
		"checksum line missing": strings.Repeat("a", 64) + "  tyci_other_file\n",
		"malformed checksum":    "xyz  " + platformAsset + "\n",
	}
	for name, sums := range tests {
		t.Run(name, func(t *testing.T) {
			useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, sums)
			dir, exe := installDir(t)

			err := runUpdate(context.Background(), &bytes.Buffer{}, updateOptions{}, "v0.8.0", exe)
			if err == nil {
				t.Fatal("runUpdate succeeded, want an error")
			}
			requireContent(t, exe, "old binary")
			requireOnly(t, dir, "tyci")
		})
	}
}

func TestRunUpdateMissingAsset(t *testing.T) {
	useFakeRelease(t, "v0.9.0", map[string][]byte{"tyci_plan9_mips": []byte("other")}, "")
	dir, exe := installDir(t)

	err := runUpdate(context.Background(), &bytes.Buffer{}, updateOptions{}, "v0.8.0", exe)
	if err == nil || !strings.Contains(err.Error(), platformAsset) {
		t.Fatalf("runUpdate error = %v, want one that names %s", err, platformAsset)
	}
	requireContent(t, exe, "old binary")
	requireOnly(t, dir, "tyci")
}

func TestRunUpdateNonWritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, "")
	dir, exe := installDir(t)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	// Restore the mode before t.TempDir removes the directory.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := runUpdate(context.Background(), &bytes.Buffer{}, updateOptions{}, "v0.8.0", exe)
	if err == nil {
		t.Fatal("runUpdate succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "by hand") || !strings.Contains(err.Error(), exe) {
		t.Fatalf("error = %v, want the manual hint with the path", err)
	}
	requireContent(t, exe, "old binary")
	requireOnly(t, dir, "tyci")
}

func TestRunUpdateCheckNeverWrites(t *testing.T) {
	downloads := useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, "")
	dir, exe := installDir(t)

	var out bytes.Buffer
	if err := runUpdate(context.Background(), &out, updateOptions{check: true}, "v0.8.0", exe); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "update available") {
		t.Fatalf("output = %q, want an update notice", out.String())
	}
	if n := downloads.Load(); n != 0 {
		t.Fatalf("--check downloaded %d assets, want 0", n)
	}
	requireContent(t, exe, "old binary")
	requireOnly(t, dir, "tyci")
}

func TestRunUpdateDevBuildNeedsForce(t *testing.T) {
	downloads := useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, "")
	dir, exe := installDir(t)

	err := runUpdate(context.Background(), &bytes.Buffer{}, updateOptions{}, "v0.6.0-22-gebb8788", exe)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("runUpdate error = %v, want a --force hint", err)
	}
	if n := downloads.Load(); n != 0 {
		t.Fatalf("dev build downloaded %d assets, want 0", n)
	}
	requireContent(t, exe, "old binary")
	requireOnly(t, dir, "tyci")
}

func TestRunUpdateAlreadyAtLatest(t *testing.T) {
	downloads := useFakeRelease(t, "v0.9.0", map[string][]byte{platformAsset: []byte("new binary")}, "")
	_, exe := installDir(t)

	var out bytes.Buffer
	if err := runUpdate(context.Background(), &out, updateOptions{}, "v0.9.0", exe); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !strings.Contains(out.String(), "already at v0.9.0") {
		t.Fatalf("output = %q, want the already-at line", out.String())
	}
	if n := downloads.Load(); n != 0 {
		t.Fatalf("up-to-date run downloaded %d assets, want 0", n)
	}
}
