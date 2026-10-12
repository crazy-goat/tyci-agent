package flow

import (
	"path/filepath"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/gitinfo"
)

// Listing is one workflow directory of the current repository, for the TUI
// "/" popup. Err is set when the workflow does not load. Source is "project"
// or "home" for a loaded workflow, and empty when it does not load.
type Listing struct {
	Name   string
	Params []Param
	Source string
	Err    error
}

// Listings returns every workflow directory that Lookup searches, sorted by
// name. A directory that does not load is listed with its error, so the
// person can see why it is not started.
func Listings(info RepoInfo) []Listing {
	var projectDirs []string
	for _, d := range gitinfo.ProjectFiles(info.Root, "workflows") {
		projectDirs = append(projectDirs, d+string(filepath.Separator))
	}
	var out []Listing
	for _, name := range availableWorkflows(info.Home, info.Root, info.Trusted) {
		wf, source, err := Lookup(name, info.Home, info.Root, info.Trusted)
		l := Listing{Name: name, Err: err}
		if err == nil {
			l.Params = wf.Params
			l.Source = "home"
			for _, d := range projectDirs {
				if strings.HasPrefix(source, d) {
					l.Source = "project"
				}
			}
		}
		out = append(out, l)
	}
	return out
}
