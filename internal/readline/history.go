package readline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

func DefaultHistoryFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".tyci", "history")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	return path, nil
}

func loadHistoryFromFile(path string, maxEntries int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ignoring corrupt history file %s: %v", path, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		if isPrintable(line) {
			clean = append(clean, line)
		}
	}
	if len(clean) > maxEntries {
		clean = clean[len(clean)-maxEntries:]
	}
	return clean, nil
}

func isPrintable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) && r != '\t' {
			return false
		}
	}
	return true
}

func dedupAdjacent(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	result := []string{lines[0]}
	for _, line := range lines[1:] {
		if line != result[len(result)-1] {
			result = append(result, line)
		}
	}
	return result
}

func appendLineToFile(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

func syncHistoryToFile(history []string, path string, maxEntries int) error {
	if len(history) > maxEntries {
		history = history[len(history)-maxEntries:]
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data := strings.Join(history, "\n") + "\n"
	return os.WriteFile(path, []byte(data), 0644)
}
