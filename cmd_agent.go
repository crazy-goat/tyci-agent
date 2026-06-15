package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/agent"
)

func runAgentSubcommand(args []string) {
	if len(args) == 0 {
		// Default: list agents
		agent.DisplayAgents()
		return
	}

	cmd := args[0]
	switch cmd {
	case "list":
		agent.DisplayAgents()

	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci agent get <name>")
			os.Exit(1)
		}
		name := args[1]
		entry, ok := agent.GetAgentEntry(name)
		if !ok {
			fmt.Fprintf(os.Stderr, "Agent %q not found\n", name)
			os.Exit(1)
		}
		if entry.Model != "" {
			fmt.Printf("%s = %s\n", name, entry.Model)
		} else {
			fmt.Printf("%s = (no model set)\n", name)
		}
		if len(entry.Fallback) > 0 {
			fmt.Printf("  fallback: %s\n", strings.Join(entry.Fallback, ", "))
		}

	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci agent set <name> --model=\"provider/model\"")
			os.Exit(1)
		}
		name := args[1]
		// Parse --model flag from remaining args
		rest := args[2:]
		model := ""
		for i, a := range rest {
			if a == "--model" || a == "-m" {
				if i+1 < len(rest) {
					model = rest[i+1]
				}
			} else if strings.HasPrefix(a, "--model=") {
				model = strings.TrimPrefix(a, "--model=")
			} else if strings.HasPrefix(a, "-m=") {
				model = strings.TrimPrefix(a, "-m=")
			}
		}
		if model == "" {
			fmt.Fprintln(os.Stderr, "Error: --model is required (format: provider/model)")
			os.Exit(1)
		}
		if err := agent.SetAgent(name, model); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Agent %q set to %s (config: %s)\n", name, model, agent.ConfigPath())

	case "delete":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci agent delete <name>")
			os.Exit(1)
		}
		name := args[1]
		if err := agent.DeleteAgent(name); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Agent %q deleted (config: %s)\n", name, agent.ConfigPath())

	case "set-fallback":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci agent set-fallback <name> --model <fallback1> [--model <fallback2> ...]")
			os.Exit(1)
		}
		name := args[1]
		rest := args[2:]

		// Parse multiple --model flags
		var fallbacks []string
		for i := 0; i < len(rest); i++ {
			a := rest[i]
			if a == "--model" || a == "-m" {
				if i+1 < len(rest) {
					fallbacks = append(fallbacks, rest[i+1])
					i++
				}
			} else if strings.HasPrefix(a, "--model=") {
				fallbacks = append(fallbacks, strings.TrimPrefix(a, "--model="))
			} else if strings.HasPrefix(a, "-m=") {
				fallbacks = append(fallbacks, strings.TrimPrefix(a, "-m="))
			}
		}

		if err := agent.SetFallback(name, fallbacks); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if len(fallbacks) == 0 {
			fmt.Fprintf(os.Stderr, "Fallback models removed for agent %q (config: %s)\n", name, agent.ConfigPath())
		} else {
			fmt.Fprintf(os.Stderr, "Agent %q fallback set to [%s] (config: %s)\n", name, strings.Join(fallbacks, ", "), agent.ConfigPath())
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown agent subcommand: %q\n", cmd)
		fmt.Fprintln(os.Stderr, "Usage: tyci agent [list|get|set|delete|set-fallback]")
		os.Exit(1)
	}
}
