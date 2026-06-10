package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci-agent/internal/connect"
)

func runProviderAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider auth [set|get|list|rm]")
		os.Exit(1)
	}

	cmd := args[0]
	switch cmd {
	case "set":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider auth set <provider> [<key>]")
			fmt.Fprintln(os.Stderr, "  If <key> is omitted, reads from stdin.")
			fmt.Fprintln(os.Stderr, "  If <key> is \"-\", reads from stdin.")
			os.Exit(1)
		}
		provider := args[1]
		var key string
		if len(args) >= 3 {
			key = args[2]
			if key == "-" {
				// Read from stdin
				data, err := readStdin()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error reading key from stdin: %v\n", err)
					os.Exit(1)
				}
				key = strings.TrimSpace(string(data))
			}
		} else {
			// Prompt for key
			fmt.Fprint(os.Stderr, "Enter API key: ")
			data, err := readStdin()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error reading key: %v\n", err)
				os.Exit(1)
			}
			key = strings.TrimSpace(string(data))
		}
		if key == "" {
			fmt.Fprintln(os.Stderr, "Error: API key cannot be empty")
			os.Exit(1)
		}
		if err := connect.SetKey(provider, key); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Saved key for provider %q in %s\n", provider, connect.AuthPath())

	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider auth get <provider>")
			os.Exit(1)
		}
		provider := args[1]
		key, ok, err := connect.GetKey(provider)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "No key found for provider %q\n", provider)
			os.Exit(1)
		}
		_, _ = fmt.Fprintln(os.Stdout, connect.MaskKey(key))

	case "list":
		keys, err := connect.ListKeys()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(keys) == 0 {
			_, _ = fmt.Fprintln(os.Stdout, "No API keys configured in auth.json")
			return
		}
		_, _ = fmt.Fprintln(os.Stdout, "Configured providers:")
		for _, p := range keys {
			key, ok, _ := connect.GetKey(p)
			masked := ""
			if ok {
				masked = connect.MaskKey(key)
			}
			_, _ = fmt.Fprintf(os.Stdout, "  %s = %s\n", p, masked)
		}

	case "rm":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider auth rm <provider>")
			os.Exit(1)
		}
		provider := args[1]
		if err := connect.RemoveKey(provider); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		_, _ = fmt.Fprintf(os.Stdout, "Removed key for provider %q\n", provider)

	default:
		fmt.Fprintf(os.Stderr, "Unknown provider auth subcommand: %q\n", cmd)
		fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider auth [set|get|list|rm]")
		os.Exit(1)
	}
}

func runProviderAdd(args []string) {
	fs := flag.NewFlagSet("provider-add", flag.ExitOnError)
	apiType := fs.String("api", "openai", "API type (openai, anthropic, gemini)")
	url := fs.String("url", "", "API base URL")
	token := fs.String("token", "", "API key or $ENV_VAR reference")
	test := fs.Bool("test", false, "Test connectivity after adding")
	testModel := fs.String("test-model", "", "Model to test with (default: first model)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Usage: tyci-agent provider add <name> --url <url> [--token <key>] [--test]")
		os.Exit(1)
	}

	name := fs.Arg(0)
	if err := connect.AddProvider(name, *apiType, *url, *token, *test, *testModel); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func runProviderRefresh(args []string) {
	fs := flag.NewFlagSet("provider-refresh", flag.ExitOnError)
	providerFilter := fs.String("provider", "", "Comma-separated list of providers to import (default: all)")
	dryRun := fs.Bool("dry-run", false, "Preview without writing")
	fs.Parse(args)

	imported, err := connect.RefreshModels(*providerFilter, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(imported) == 0 {
		fmt.Fprintln(os.Stdout, "No providers found to import")
		return
	}

	if *dryRun {
		fmt.Fprintf(os.Stdout, "Would import %d providers:\n\n", len(imported))
	} else {
		fmt.Fprintf(os.Stdout, "Imported %d providers:\n\n", len(imported))
	}

	for _, p := range imported {
		fmt.Fprintf(os.Stdout, "  %s (%s): %d models\n", p.Name, p.Type, p.Models)
	}

	if !*dryRun {
		fmt.Fprintf(os.Stdout, "\n✓ Models saved to %s\n", connect.ModelJSONPath())
	}
}

// readStdin reads all data from stdin until EOF.
// For pipe input (e.g., echo "key" | tyci-agent ...), reads everything.
// For terminal input, reads one line.
func readStdin() ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
		// Pipe mode: read all until EOF
		buf := make([]byte, 0, 4096)
		tmp := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				return nil, err
			}
		}
		return buf, nil
	}
	// Terminal mode: read one line
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return []byte(scanner.Text()), nil
	}
	return nil, scanner.Err()
}
