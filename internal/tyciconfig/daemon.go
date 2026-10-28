package tyciconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// Defaults and limits of the "daemon" section.
const (
	DefaultPollInterval = 60 * time.Second
	MinPollInterval     = 5 * time.Second
	DefaultDaemonWorkers = 3
)

// DaemonTriggersFile is the "daemon.triggers" object. A nil field is unset.
type DaemonTriggersFile struct {
	Issue     *bool `json:"issue"`
	CIFailure *bool `json:"ci_failure"`
	Cron      *bool `json:"cron"`
}

// DaemonFile is the "daemon" section of a config file. A nil field is unset.
type DaemonFile struct {
	Enabled      *bool              `json:"enabled"`
	PollInterval *string            `json:"poll_interval"`
	Workers      *int               `json:"workers"`
	Triggers     DaemonTriggersFile `json:"triggers"`
	Repos        *[]string          `json:"repos"`
}

// Daemon is the resolved "daemon" section.
type Daemon struct {
	Enabled      bool
	PollInterval time.Duration
	Workers      int // 0 = unlimited
	Issue        bool
	CIFailure    bool
	Cron         bool
	// Repos are the "owner/name" repositories the issue and ci_failure triggers
	// watch. Without repos the daemon runs cron only.
	Repos []string
}

// MergeDaemon returns user with every key set in project replaced (project wins).
func MergeDaemon(user, project DaemonFile) DaemonFile {
	if project.Enabled != nil {
		user.Enabled = project.Enabled
	}
	if project.PollInterval != nil {
		user.PollInterval = project.PollInterval
	}
	if project.Workers != nil {
		user.Workers = project.Workers
	}
	if project.Triggers.Issue != nil {
		user.Triggers.Issue = project.Triggers.Issue
	}
	if project.Triggers.CIFailure != nil {
		user.Triggers.CIFailure = project.Triggers.CIFailure
	}
	if project.Triggers.Cron != nil {
		user.Triggers.Cron = project.Triggers.Cron
	}
	if project.Repos != nil {
		user.Repos = project.Repos
	}
	return user
}

// Resolve applies the defaults (enabled, 60s, 3 workers, all triggers on, no
// repos) and validates the values. source names the file in error messages.
func (c DaemonFile) Resolve(source string) (Daemon, error) {
	d := Daemon{Enabled: true, PollInterval: DefaultPollInterval, Workers: DefaultDaemonWorkers, Issue: true, CIFailure: true, Cron: true}
	if c.Enabled != nil {
		d.Enabled = *c.Enabled
	}
	if c.PollInterval != nil {
		v, err := time.ParseDuration(*c.PollInterval)
		if err != nil || v < MinPollInterval {
			return Daemon{}, fmt.Errorf("daemon.poll_interval must be a duration of at least %s, for example \"60s\" (got %q) in %s", MinPollInterval, *c.PollInterval, source)
		}
		d.PollInterval = v
	}
	if c.Workers != nil {
		if *c.Workers < 0 {
			return Daemon{}, fmt.Errorf("daemon.workers must be an integer >= 0 (got %d) in %s", *c.Workers, source)
		}
		d.Workers = *c.Workers
	}
	for _, t := range []struct {
		v   *bool
		dst *bool
	}{{c.Triggers.Issue, &d.Issue}, {c.Triggers.CIFailure, &d.CIFailure}, {c.Triggers.Cron, &d.Cron}} {
		if t.v != nil {
			*t.dst = *t.v
		}
	}
	if c.Repos != nil {
		for _, r := range *c.Repos {
			owner, name, ok := strings.Cut(r, "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
				return Daemon{}, fmt.Errorf("daemon.repos: %q is not \"owner/name\" in %s", r, source)
			}
		}
		d.Repos = append([]string(nil), *c.Repos...)
	}
	return d, nil
}

// LoadDaemon reads the daemon section of the user and project config files (a
// missing file or path "" is fine), merges them key by key and resolves it.
func LoadDaemon(userPath, projectPath string) (Daemon, error) {
	user, err := readDaemonFile(userPath)
	if err != nil {
		return Daemon{}, err
	}
	project, err := readDaemonFile(projectPath)
	if err != nil {
		return Daemon{}, err
	}
	// Name the file that sets an invalid key; the project file wins.
	if _, err := project.Resolve(projectPath); err != nil {
		return Daemon{}, err
	}
	return MergeDaemon(user, project).Resolve(userPath)
}

func readDaemonFile(path string) (DaemonFile, error) {
	if path == "" {
		return DaemonFile{}, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return DaemonFile{}, nil
	}
	if err != nil {
		return DaemonFile{}, fmt.Errorf("%s: %w", path, err)
	}
	var top struct {
		Daemon json.RawMessage `json:"daemon"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return DaemonFile{}, fmt.Errorf("%s: %w", path, err)
	}
	var fc DaemonFile
	if len(top.Daemon) == 0 {
		return fc, nil
	}
	if err := json.Unmarshal(top.Daemon, &fc); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return DaemonFile{}, fmt.Errorf("daemon.%s must be %s in %s", te.Field, te.Type, path)
		}
		return DaemonFile{}, fmt.Errorf("daemon in %s: %w", path, err)
	}
	return fc, nil
}
