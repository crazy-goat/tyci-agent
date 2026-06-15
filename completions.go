package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/internal/connect"
	"github.com/decodo/tyci/providers"
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

// completeProviderModels returns "provider/model" entries from the configured
// model registry. Used for --model flag completion.
func completeProviderModels(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	cfg, err := providers.LoadConfig(modelJSONPath())
	if err != nil || cfg == nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	models := make([]string, 0)
	for prov, entries := range cfg {
		seen := map[string]struct{}{}
		for _, m := range entries {
			key := prov + "/" + m.Name
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if toComplete == "" || strings.HasPrefix(key, toComplete) {
				models = append(models, key)
			}
		}
	}
	sort.Strings(models)
	return models, cobra.ShellCompDirectiveNoFileComp
}

// completeAgents returns agent names from the agents config.
func completeAgents(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	_ = agent.ConfigPath()

	names, err := agent.ListAgents()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if toComplete == "" || strings.HasPrefix(n, toComplete) {
			out = append(out, n)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
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
