package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/gitinfo"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
	"github.com/crazy-goat/tyci-agent/internal/mcp"
	"github.com/crazy-goat/tyci-agent/tools"
)

// projectSetup is the project setup of this process. initCommon records it
// once; /reload and the settings watcher repeat it, so they never make a
// second trust decision.
var projectSetup struct {
	mu      sync.Mutex
	set     bool
	wd      string
	trusted bool
	mcp     bool // MCP servers are connected for this process
}

// reloadMu serializes reloads, so /reload and the watcher never interleave.
var reloadMu sync.Mutex

// rememberProjectSetup records the project setup that initCommon made. It
// also records the file hashes that the setup loaded, so that the first
// reload sees only the changes made after the start.
func rememberProjectSetup(wd string, trusted, mcp bool) {
	reloadMu.Lock()
	reloadHashes = settingsHashes(wd, trusted)
	reloadMu.Unlock()

	projectSetup.mu.Lock()
	defer projectSetup.mu.Unlock()
	projectSetup.set = true
	projectSetup.wd = wd
	projectSetup.trusted = trusted
	projectSetup.mcp = mcp
}

// reloadArea is the result of reloading one part of the settings. A failed
// area keeps its previous settings. changed is true when its files differ
// from the last successful load.
type reloadArea struct {
	name    string
	detail  string
	changed bool
	err     error
}

// reloadHashes holds, per area, the hash of its files at the last successful
// load of that area. Guarded by reloadMu.
var reloadHashes = map[string]string{}

// settingsPaths lists the files and directories that a reload reads, for the
// trust decision of the project.
func settingsPaths(wd string, trusted bool) (hookPaths, luaDirs, mcpPaths []string) {
	hookPaths = hooks.DefaultPaths(wd)
	luaDirs = []string{filepath.Join(os.Getenv("HOME"), ".tyci", "tools")}
	mcpPaths = []string{mcp.ConfigPath()}
	if !trusted {
		return hookPaths[:1], luaDirs, mcpPaths
	}
	luaDirs = append(luaDirs, gitinfo.ProjectPath(wd, "tools"))
	mcpPaths = append(mcpPaths, gitinfo.ProjectPath(wd, "mcp.json"))
	return hookPaths, luaDirs, mcpPaths
}

// settingsHashes returns the content hash of the files of the hooks, Lua
// tools and MCP areas.
func settingsHashes(wd string, trusted bool) map[string]string {
	hookPaths, luaDirs, mcpPaths := settingsPaths(wd, trusted)
	var luaFiles []string
	for _, dir := range luaDirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".lua") {
				luaFiles = append(luaFiles, filepath.Join(dir, e.Name()))
			}
		}
	}
	return map[string]string{
		"hooks":     fileHash(hookPaths),
		"lua tools": fileHash(luaFiles),
		"mcp":       fileHash(mcpPaths),
	}
}

// fileHash returns a hash of the content of paths. A missing file counts as
// its name and "missing".
func fileHash(paths []string) string {
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00", p)
		data, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprint(h, "missing\x00")
			continue
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// reloadAreas reloads hooks, Lua tools, the cron dir and MCP servers. It does
// not change the trust decision. The MCP servers restart only when an MCP
// file changed. Each area is all or nothing, except Lua tools, which are per
// file.
func reloadAreas(ctx context.Context) ([]reloadArea, error) {
	reloadMu.Lock()
	defer reloadMu.Unlock()

	projectSetup.mu.Lock()
	set, wd, trusted, mcp := projectSetup.set, projectSetup.wd, projectSetup.trusted, projectSetup.mcp
	projectSetup.mu.Unlock()
	if !set {
		return nil, errors.New("reload: the project is not set up")
	}

	hookPaths, _, _ := settingsPaths(wd, trusted)
	hashes := settingsHashes(wd, trusted)
	changed := func(name string) bool {
		old, ok := reloadHashes[name]
		return !ok || old != hashes[name]
	}

	var areas []reloadArea

	if loaded, errs := hooks.Read(hookPaths...); len(errs) > 0 {
		areas = append(areas, reloadArea{name: "hooks", detail: "kept previous settings", err: errors.Join(errs...)})
	} else {
		hooks.Set(loaded)
		areas = append(areas, reloadArea{name: "hooks", detail: fmt.Sprintf("%d loaded", len(loaded)), changed: changed("hooks")})
	}

	localTools := ""
	if trusted {
		localTools = gitinfo.ProjectPath(wd, "tools")
	}
	n, err := tools.ReloadLuaTools(localTools)
	areas = append(areas, reloadArea{name: "lua tools", detail: fmt.Sprintf("%d loaded", n), changed: changed("lua tools"), err: err})

	cronDir, cronDetail := "", "global dir only"
	if trusted {
		cronDir = filepath.Dir(gitinfo.ProjectPath(wd, "cron.json"))
		cronDetail = "global and project dirs"
	}
	tools.SetLocalCronDir(cronDir)
	areas = append(areas, reloadArea{name: "cron", detail: cronDetail})

	if mcp {
		if changed("mcp") {
			n, err := tools.ReloadMCP(ctx, wd, trusted)
			area := reloadArea{name: "mcp", detail: fmt.Sprintf("%d server(s) restarted", n), changed: true, err: err}
			if err != nil {
				area.detail = "kept previous settings"
			}
			areas = append(areas, area)
		} else {
			areas = append(areas, reloadArea{name: "mcp", detail: "unchanged, not restarted"})
		}
	}

	var errs []error
	for _, a := range areas {
		if a.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.name, a.err))
			continue
		}
		reloadHashes[a.name] = hashes[a.name]
	}
	return areas, errors.Join(errs...)
}

// reloadProjectSettings reloads the settings and returns one line per area.
// An area that failed keeps its previous settings and its error is returned.
func reloadProjectSettings(ctx context.Context) (string, error) {
	areas, err := reloadAreas(ctx)
	if areas == nil {
		return "", err
	}
	lines := make([]string, 0, len(areas))
	for _, a := range areas {
		if a.err != nil {
			lines = append(lines, fmt.Sprintf("%s: %s (%v)", a.name, a.detail, a.err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", a.name, a.detail))
	}
	return strings.Join(lines, "\n"), err
}

// reloadChangedAreas is reloadProjectSettings for the settings watcher. It
// returns the names of the areas that changed and reloaded, in one line.
func reloadChangedAreas(ctx context.Context) (string, error) {
	areas, err := reloadAreas(ctx)
	if areas == nil {
		return "", err
	}
	var names []string
	for _, a := range areas {
		if a.changed && a.err == nil {
			names = append(names, a.name)
		}
	}
	return strings.Join(names, ", "), err
}

// Polling of the settings files for defaults.watch_config. A change must stay
// unchanged for watchDebounce before the reload runs.
var (
	watchPollInterval = 2 * time.Second
	watchDebounce     = 500 * time.Millisecond
)

// settingsFingerprint describes the size and modification time of every
// file and every entry of a directory that a reload reads. A changed
// fingerprint means the settings may have changed.
func settingsFingerprint(wd string, trusted bool) string {
	home := os.Getenv("HOME")
	files := []string{
		filepath.Join(home, ".tyci", "hooks.json"),
		filepath.Join(home, ".tyci", "cron.json"),
		mcp.ConfigPath(),
	}
	dirs := []string{filepath.Join(home, ".tyci", "tools")}
	if trusted {
		files = append(files, gitinfo.ProjectPath(wd, "hooks.json"), gitinfo.ProjectPath(wd, "cron.json"), gitinfo.ProjectPath(wd, "mcp.json"))
		dirs = append(dirs, gitinfo.ProjectPath(wd, "tools"))
	}

	var b strings.Builder
	for _, f := range files {
		writeStat(&b, f)
	}
	for _, d := range dirs {
		entries, _ := os.ReadDir(d)
		for _, e := range entries {
			writeStat(&b, filepath.Join(d, e.Name()))
		}
	}
	return b.String()
}

// writeStat appends one line for path to b: its size and modification time,
// or "missing".
func writeStat(b *strings.Builder, path string) {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(b, "%s missing\n", path)
		return
	}
	fmt.Fprintf(b, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
}

// watchSettings calls reload once fingerprint has changed and then stayed
// unchanged for watchDebounce. It returns when ctx ends.
func watchSettings(ctx context.Context, fingerprint func() string, reload func()) {
	ticker := time.NewTicker(watchPollInterval)
	defer ticker.Stop()
	last := fingerprint()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur := fingerprint()
		if cur == last {
			continue
		}
		// Wait until the files stop changing, then reload once.
		for {
			last = cur
			select {
			case <-ctx.Done():
				return
			case <-time.After(watchDebounce):
			}
			cur = fingerprint()
			if cur == last {
				break
			}
		}
		reload()
	}
}

// watchProjectSettings watches the settings of this process and reloads them
// on change. report receives the names of the areas that changed. It returns
// when ctx ends.
func watchProjectSettings(ctx context.Context, report func(changed string, err error)) {
	projectSetup.mu.Lock()
	set, wd, trusted := projectSetup.set, projectSetup.wd, projectSetup.trusted
	projectSetup.mu.Unlock()
	if !set {
		return
	}
	watchSettings(ctx,
		func() string { return settingsFingerprint(wd, trusted) },
		func() { report(reloadChangedAreas(ctx)) },
	)
}
