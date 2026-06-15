package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/decodo/tyci/internal/connect"
)

var (
	errProviderUsage     = errors.New("Usage: tyci provider [add|refresh|auth]")
	errProviderAuthUsage = errors.New("Usage: tyci provider auth [set|get|list|rm]")
)

func runProviderAdd(name, apiType, url, token string, test bool, testModel string) error {
	return connect.AddProvider(name, apiType, url, token, test, testModel)
}

func runProviderRefresh(providerFilter string, dryRun bool) error {
	imported, err := connect.RefreshModels(providerFilter, dryRun)
	if err != nil {
		return err
	}

	if len(imported) == 0 {
		fmt.Fprintln(os.Stdout, "No providers found to import")
		return nil
	}

	if dryRun {
		fmt.Fprintf(os.Stdout, "Would import %d providers:\n\n", len(imported))
	} else {
		fmt.Fprintf(os.Stdout, "Imported %d providers:\n\n", len(imported))
	}

	for _, p := range imported {
		fmt.Fprintf(os.Stdout, "  %s (%s): %d models\n", p.Name, p.Type, p.Models)
	}

	if !dryRun {
		fmt.Fprintf(os.Stdout, "\n✓ Models saved to %s\n", connect.ModelJSONPath())
	}
	return nil
}

func runProviderAuthSet(provider, key string) error {
	if key == "-" {
		data, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading key from stdin: %w", err)
		}
		key = strings.TrimSpace(string(data))
	} else if key == "" {
		fmt.Fprint(os.Stderr, "Enter API key: ")
		data, err := readStdin()
		if err != nil {
			return fmt.Errorf("reading key: %w", err)
		}
		key = strings.TrimSpace(string(data))
	}
	if key == "" {
		return errors.New("API key cannot be empty")
	}
	if err := connect.SetKey(provider, key); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "Saved key for provider %q in %s\n", provider, connect.AuthPath())
	return nil
}

func runProviderAuthGet(provider string) error {
	key, ok, err := connect.GetKey(provider)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("No key found for provider %q", provider)
	}
	_, _ = fmt.Fprintln(os.Stdout, connect.MaskKey(key))
	return nil
}

func runProviderAuthList() error {
	keys, err := connect.ListKeys()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "No API keys configured in auth.json")
		return nil
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
	return nil
}

func runProviderAuthRm(provider string) error {
	if err := connect.RemoveKey(provider); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "Removed key for provider %q\n", provider)
	return nil
}

// readStdin reads all data from stdin until EOF.
// For pipe input (e.g., echo "key" | tyci ...), reads everything.
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
