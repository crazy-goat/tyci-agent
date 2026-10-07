package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// FileConfig is the "orchestrator" section of a config file. A nil field is unset.
type FileConfig struct {
	Workers        *int    `json:"workers"`
	PlanTimeoutSec *int    `json:"plan_timeout_sec"`
	AcceptedLabel  *string `json:"accepted_label"`
}

// Merge returns user with every key set in project replaced (project wins).
func Merge(user, project FileConfig) FileConfig {
	if project.Workers != nil {
		user.Workers = project.Workers
	}
	if project.PlanTimeoutSec != nil {
		user.PlanTimeoutSec = project.PlanTimeoutSec
	}
	if project.AcceptedLabel != nil {
		user.AcceptedLabel = project.AcceptedLabel
	}
	return user
}

// Resolve applies the defaults (3 workers, no plan timeout, label "accepted")
// and validates the values. source names the file in error messages.
func (c FileConfig) Resolve(source string) (Config, error) {
	cfg := Config{Workers: 3, AcceptedLabel: "accepted"}
	if c.Workers != nil {
		if *c.Workers < 0 {
			return Config{}, fmt.Errorf("orchestrator.workers must be an integer >= 0 (got %d) in %s", *c.Workers, source)
		}
		cfg.Workers = *c.Workers
	}
	if c.PlanTimeoutSec != nil {
		if *c.PlanTimeoutSec < 0 {
			return Config{}, fmt.Errorf("orchestrator.plan_timeout_sec must be an integer >= 0 (got %d) in %s", *c.PlanTimeoutSec, source)
		}
		cfg.PlanTimeout = time.Duration(*c.PlanTimeoutSec) * time.Second
	}
	if c.AcceptedLabel != nil {
		if *c.AcceptedLabel == "" {
			return Config{}, fmt.Errorf("orchestrator.accepted_label must be a non-empty string in %s", source)
		}
		cfg.AcceptedLabel = *c.AcceptedLabel
	}
	return cfg, nil
}

// LoadConfig reads the orchestrator section of the user and project config
// files (a missing file is fine), merges them key by key and resolves it.
func LoadConfig(userPath, projectPath string) (Config, error) {
	user, err := readFileConfig(userPath)
	if err != nil {
		return Config{}, err
	}
	project, err := readFileConfig(projectPath)
	if err != nil {
		return Config{}, err
	}
	merged := Merge(user, project)
	// Name the file that sets an invalid key; the project file wins.
	if _, err := project.Resolve(projectPath); err != nil {
		return Config{}, err
	}
	return merged.Resolve(userPath)
}

func readFileConfig(path string) (FileConfig, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return FileConfig{}, nil
	}
	if err != nil {
		return FileConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	var top struct {
		Orchestrator json.RawMessage `json:"orchestrator"`
	}
	if err := json.Unmarshal(b, &top); err != nil {
		return FileConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	var fc FileConfig
	if len(top.Orchestrator) == 0 {
		return fc, nil
	}
	if err := json.Unmarshal(top.Orchestrator, &fc); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return FileConfig{}, fmt.Errorf("orchestrator.%s must be %s in %s", te.Field, te.Type, path)
		}
		return FileConfig{}, fmt.Errorf("orchestrator in %s: %w", path, err)
	}
	return fc, nil
}
