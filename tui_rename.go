package main

import (
	"errors"
	"fmt"

	"github.com/crazy-goat/tyci-agent/session"
)

// renameUsage is the reply to "/rename" with no title. The command does not
// ask the model for a title: the TUI has no API for a hidden prompt.
const renameUsage = "usage: /rename <title>"

// parseRenameArgs cleans the argument of "/rename" with session.CleanTitle.
// An empty result gives renameUsage as the error.
func parseRenameArgs(arg string) (string, error) {
	title := session.CleanTitle(arg)
	if title == "" {
		return "", errors.New(renameUsage)
	}
	return title, nil
}

// handleRenameCommand implements "/rename <title>". rename stores the title
// in the session. It reports whether the title was stored.
func handleRenameCommand(disp slashCommandDisplay, arg string, rename func(title string) (string, error)) bool {
	disp.ResetStatus()
	title, err := parseRenameArgs(arg)
	if err != nil {
		disp.ToolBlock(err.Error())
		return false
	}
	stored, err := rename(title)
	if err != nil {
		disp.Error(fmt.Errorf("/rename: %v", err))
		return false
	}
	disp.ToolBlock("renamed: " + stored)
	return true
}
