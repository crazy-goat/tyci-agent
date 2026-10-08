package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crazy-goat/tyci-agent/internal/connect"
	"github.com/crazy-goat/tyci-agent/providers"
	"github.com/spf13/cobra"
)

func tyciHomeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".tyci")
	}
	return filepath.Join("~", ".tyci")
}

func modelJSONPath() string {
	if v := os.Getenv("TYCI_MODEL_JSON"); v != "" {
		return v
	}
	return filepath.Join(tyciHomeDir(), "model.json")
}

// completeProviderNames returns provider names known to the auth store
// and the model registry. Used for positional args where a provider is expected.
func completeProviderNames(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	seen := map[string]struct{}{}
	var out []string

	if cfg, err := providers.LoadConfig(modelJSONPath()); err == nil {
		for prov := range cfg {
			if _, ok := seen[prov]; ok {
				continue
			}
			seen[prov] = struct{}{}
			if toComplete == "" || strings.HasPrefix(prov, toComplete) {
				out = append(out, prov)
			}
		}
	}

	if keys, err := connect.ListKeys(); err == nil {
		for _, p := range keys {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			if toComplete == "" || strings.HasPrefix(p, toComplete) {
				out = append(out, p)
			}
		}
	}

	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}
