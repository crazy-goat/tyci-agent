package display

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
)

// tuiStateFile is the name of the TUI's own state file in the tyci home
// directory. The TUI writes it; config.json is edited by hand and is never
// written for these values.
const tuiStateFile = "tui-state.json"

// defaultSidebarWidthPercent is the sidebar width used when no valid value is
// saved.
const defaultSidebarWidthPercent = 40.0

// tuiState is the content of tuiStateFile.
type tuiState struct {
	SidebarWidthPercent float64 `json:"sidebar_width_percent"`
}

// tuiStatePath returns ~/.tyci/tui-state.json. It follows $HOME, so a test
// can point it at a temporary directory with t.Setenv("HOME", ...).
func tuiStatePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tyci", tuiStateFile), nil
}

// loadSidebarWidthPercent returns the saved sidebar width. A missing,
// unreadable or invalid file, or a value outside (0, 100), gives the default.
// It is called once, when the TUI starts; a running TUI never reloads it.
func loadSidebarWidthPercent() float64 {
	path, err := tuiStatePath()
	if err != nil {
		return defaultSidebarWidthPercent
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultSidebarWidthPercent
	}
	var st tuiState
	if json.Unmarshal(data, &st) != nil {
		return defaultSidebarWidthPercent
	}
	if !(st.SidebarWidthPercent > 0 && st.SidebarWidthPercent < 100) {
		return defaultSidebarWidthPercent
	}
	return st.SidebarWidthPercent
}

// roundPercent keeps three decimals. At that precision the column count
// round(width * percent / 100) is the same as the one that was saved, for any
// terminal width below about 100000 columns.
func roundPercent(percent float64) float64 {
	return math.Round(percent*1000) / 1000
}

// saveSidebarWidthPercent writes the state file atomically: it writes a temp
// file in the same directory, then renames it over the old file. A reader
// sees either the old file or the new one, never a partial file.
func saveSidebarWidthPercent(percent float64) error {
	path, err := tuiStatePath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(tuiState{SidebarWidthPercent: roundPercent(percent)})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, tuiStateFile+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}
