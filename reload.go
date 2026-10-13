package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crazy-goat/tyci-agent/internal/gitinfo"
	"github.com/crazy-goat/tyci-agent/internal/hooks"
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

// rememberProjectSetup records the project setup that initCommon made.
func rememberProjectSetup(wd string, trusted, mcp bool) {
	projectSetup.mu.Lock()
	defer projectSetup.mu.Unlock()
	projectSetup.set = true
	projectSetup.wd = wd
	projectSetup.trusted = trusted
	projectSetup.mcp = mcp
}

// reloadArea is the result of reloading one part of the settings. A failed
// area keeps its previous settings.
type reloadArea struct {
	name   string
	detail string
	err    error
}

// reloadAreas reloads hooks, Lua tools, the cron dir and MCP servers. It does
// not change the trust decision. Each area is all or nothing.
func reloadAreas(ctx context.Context) ([]reloadArea, error) {
	reloadMu.Lock()
	defer reloadMu.Unlock()

	projectSetup.mu.Lock()
	set, wd, trusted, mcp := projectSetup.set, projectSetup.wd, projectSetup.trusted, projectSetup.mcp
	projectSetup.mu.Unlock()
	if !set {
		return nil, errors.New("reload: the project is not set up")
	}

	var areas []reloadArea

	hookPaths := hooks.DefaultPaths(wd)
	if !trusted {
		hookPaths = hookPaths[:1]
	}
	if loaded, errs := hooks.Read(hookPaths...); len(errs) > 0 {
		areas = append(areas, reloadArea{name: "hooks", err: errors.Join(errs...)})
	} else {
		hooks.Set(loaded)
		areas = append(areas, reloadArea{name: "hooks", detail: fmt.Sprintf("%d loaded", len(loaded))})
	}

	localTools := ""
	if trusted {
		localTools = gitinfo.ProjectPath(wd, "tools")
	}
	n, err := tools.ReloadLuaTools(localTools)
	areas = append(areas, reloadArea{name: "lua tools", detail: fmt.Sprintf("%d loaded", n), err: err})

	cronDir, cronDetail := "", "global dir only"
	if trusted {
		cronDir = filepath.Dir(gitinfo.ProjectPath(wd, "cron.json"))
		cronDetail = "global and project dirs"
	}
	tools.SetLocalCronDir(cronDir)
	areas = append(areas, reloadArea{name: "cron", detail: cronDetail})

	if mcp {
		n, err := tools.ReloadMCP(ctx, wd, trusted)
		areas = append(areas, reloadArea{name: "mcp", detail: fmt.Sprintf("%d server(s) restarted", n), err: err})
	}

	var errs []error
	for _, a := range areas {
		if a.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.name, a.err))
		}
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
			lines = append(lines, fmt.Sprintf("%s: kept previous settings (%v)", a.name, a.err))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", a.name, a.detail))
	}
	return strings.Join(lines, "\n"), err
}
