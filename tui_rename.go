package main

import (
	"errors"
	"fmt"
	"strings"
)

// renameUsage is the reply to "/rename" with no title. The command does not
// ask the model for a title: the TUI has no API for a hidden prompt.
const renameUsage = "usage: /rename <title>"

// renameMaxRunes is the longest title /rename keeps. Longer input is cut.
const renameMaxRunes = 80

// parseRenameArgs trims the argument of "/rename" and cuts it to
// renameMaxRunes runes. An empty argument gives renameUsage as the error.
func parseRenameArgs(arg string) (string, error) {
	title := strings.TrimSpace(arg)
	if title == "" {
		return "", errors.New(renameUsage)
	}
	runes := []rune(title)
	if len(runes) > renameMaxRunes {
		title = string(runes[:renameMaxRunes])
	}
	return title, nil
}

// handleRenameCommand implements "/rename <title>". rename stores the title
// in the session. It reports whether the title was stored.
func handleRenameCommand(disp slashCommandDisplay, arg string, rename func(title string) error) bool {
	disp.ResetStatus()
	title, err := parseRenameArgs(arg)
	if err != nil {
		disp.ToolBlock(err.Error())
		return false
	}
	if err := rename(title); err != nil {
		disp.Error(fmt.Errorf("/rename: %v", err))
		return false
	}
	disp.ToolBlock("renamed: " + title)
	return true
}
