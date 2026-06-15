package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/agent"
)

// errAgentSetRequiresModel is returned by `tyci agent set` when --model is
// missing or empty. Kept as a sentinel so the cobra command can return it
// directly while tests assert on the message text.
var errAgentSetRequiresModel = errors.New("--model is required (format: provider/model)")

func runAgentList() error {
	return agent.DisplayAgents()
}

func runAgentGet(name string) error {
	entry, ok := agent.GetAgentEntry(name)
	if !ok {
		return fmt.Errorf("Agent %q not found", name)
	}
	if entry.Model != "" {
		fmt.Printf("%s = %s\n", name, entry.Model)
	} else {
		fmt.Printf("%s = (no model set)\n", name)
	}
	if len(entry.Fallback) > 0 {
		fmt.Printf("  fallback: %s\n", strings.Join(entry.Fallback, ", "))
	}
	return nil
}

func runAgentSet(name, model string) error {
	if model == "" {
		return errAgentSetRequiresModel
	}
	if err := agent.SetAgent(name, model); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Agent %q set to %s (config: %s)\n", name, model, agent.ConfigPath())
	return nil
}

func runAgentDelete(name string) error {
	if err := agent.DeleteAgent(name); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Agent %q deleted (config: %s)\n", name, agent.ConfigPath())
	return nil
}

func runAgentSetFallback(name string, fallbacks []string) error {
	if err := agent.SetFallback(name, fallbacks); err != nil {
		return err
	}
	if len(fallbacks) == 0 {
		fmt.Fprintf(os.Stderr, "Fallback models removed for agent %q (config: %s)\n", name, agent.ConfigPath())
	} else {
		fmt.Fprintf(os.Stderr, "Agent %q fallback set to [%s] (config: %s)\n", name, strings.Join(fallbacks, ", "), agent.ConfigPath())
	}
	return nil
}
