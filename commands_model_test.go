package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crazy-goat/tyci-agent/providers"
	"github.com/spf13/cobra"
)

func TestInitCommonNoDefaultModel(t *testing.T) {
	writeWiringTestHome(t)

	cmd := newInitCommonTestCmd(t)
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp: %v", err)
	}

	_, _, _, _, _, _, _, _, _, err := initCommon(cmd, false, false)
	if err == nil {
		t.Fatal("expected an error when default_model is not set")
	}
	if !strings.Contains(err.Error(), `"default_model"`) {
		t.Errorf("error should name default_model, got: %v", err)
	}
}

func TestInitCommonUsesDefaultModel(t *testing.T) {
	writeWiringTestHome(t)
	providers.Register(&fakeProvider{name: "defmodel-prov", configured: true, models: []string{"dm1"}})
	setDefaultModel(t, "defmodel-prov/dm1")

	cmd := newInitCommonTestCmd(t)
	if err := cmd.Flags().Set("no-mcp", "true"); err != nil {
		t.Fatalf("set no-mcp: %v", err)
	}

	prov, modelName, _, _, _, _, _, dl, shutdown, err := initCommon(cmd, false, false)
	if err != nil {
		t.Fatalf("initCommon: %v", err)
	}
	t.Cleanup(shutdown)
	if dl != nil {
		t.Cleanup(func() { dl.Close() })
	}
	if prov.Name() != "defmodel-prov" {
		t.Errorf("provider = %q, want defmodel-prov", prov.Name())
	}
	if modelName != "dm1" {
		t.Errorf("model = %q, want dm1", modelName)
	}
}

func TestRootHasNoModelOrAgentFlag(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, name := range []string{"model", "agent"} {
			if c.Flags().Lookup(name) != nil || c.PersistentFlags().Lookup(name) != nil {
				t.Errorf("%s must not have the --%s flag", c.CommandPath(), name)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

func TestAgentCommandRemoved(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "agent" {
			t.Fatal("the agent command must not be registered; use default_model in ~/.tyci/config.json")
		}
	}
}

func TestLocalModelJSONIgnored(t *testing.T) {
	writeWiringTestHome(t)

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".tyci"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	local := `{"local-only-prov": {"local-model": {"uri": "openai://local-model@$KEY@example.com/v1"}}}`
	if err := os.WriteFile(filepath.Join(project, ".tyci", "model.json"), []byte(local), 0o600); err != nil {
		t.Fatalf("write project model.json: %v", err)
	}
	t.Chdir(project)

	global := `{"global-only-prov": {"global-model": {"uri": "openai://global-model@$KEY@example.com/v1"}}}`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".tyci", "model.json"), []byte(global), 0o600); err != nil {
		t.Fatalf("write global model.json: %v", err)
	}

	registerProviders()

	if _, _, ok := providers.FindModel("global-only-prov/global-model"); !ok {
		t.Fatal("control: the global ~/.tyci/model.json must register its models")
	}
	if _, _, ok := providers.FindModel("local-only-prov/local-model"); ok {
		t.Error("the project-local .tyci/model.json must not register models")
	}
}
