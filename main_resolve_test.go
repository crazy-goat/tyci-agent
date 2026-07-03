package main

import (
	"context"
	"testing"

	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/stream"
)

// fakeProvider is a minimal providers.Provider for exercising
// resolveProviderModel without any network access.
type fakeProvider struct {
	name       string
	configured bool
	models     []string
	free       []string
}

func (f *fakeProvider) Name() string         { return f.name }
func (f *fakeProvider) IsConfigured() bool    { return f.configured }
func (f *fakeProvider) Models() []string      { return f.models }
func (f *fakeProvider) FreeModels() []string  { return f.free }
func (f *fakeProvider) Stream(context.Context, providers.Request) (<-chan stream.Event, error) {
	return nil, nil
}

func TestResolveProviderModel_ExplicitOverride(t *testing.T) {
	prov := &fakeProvider{name: "explicit-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	got, model, err := resolveProviderModel(context.Background(), "explicit-prov/m1")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != prov {
		t.Errorf("expected provider %q, got %v", prov.name, got)
	}
	if model != "m1" {
		t.Errorf("expected model %q, got %q", "m1", model)
	}
}

func TestResolveProviderModel_UnknownExplicitOverride(t *testing.T) {
	_, _, err := resolveProviderModel(context.Background(), "does-not-exist/m1")
	if err == nil {
		t.Fatal("expected error for unknown provider in explicit override")
	}
}

// TestResolveProviderModel_InheritsParentProvider is the regression test for the
// auth bug: with a bare model name, the subagent must reuse the parent's
// provider from context — NOT re-guess via FindModel, which iterates the
// provider map in random order and could land on a different provider that
// happens to list the same model (and lacks a valid key).
func TestResolveProviderModel_InheritsParentProvider(t *testing.T) {
	parent := &fakeProvider{name: "parent-prov", configured: true, models: []string{"shared-model"}}
	other := &fakeProvider{name: "other-prov", configured: true, models: []string{"shared-model"}}
	providers.Register(parent)
	providers.Register(other)

	ctx := providers.WithProvider(context.Background(), parent)
	ctx = providers.WithModel(ctx, "shared-model")

	got, model, err := resolveProviderModel(ctx, "shared-model")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != parent {
		t.Errorf("expected parent provider to be inherited, got %v", got)
	}
	if model != "shared-model" {
		t.Errorf("expected model %q, got %q", "shared-model", model)
	}
}

func TestResolveProviderModel_EmptyModelUsesContext(t *testing.T) {
	parent := &fakeProvider{name: "ctx-prov", configured: true, models: []string{"ctx-model"}}
	providers.Register(parent)

	ctx := providers.WithProvider(context.Background(), parent)
	ctx = providers.WithModel(ctx, "ctx-model")

	got, model, err := resolveProviderModel(ctx, "")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != parent {
		t.Errorf("expected context provider, got %v", got)
	}
	if model != "ctx-model" {
		t.Errorf("expected model from context %q, got %q", "ctx-model", model)
	}
}

func TestResolveProviderModel_NoContextFallsBackToLookup(t *testing.T) {
	prov := &fakeProvider{name: "lookup-prov", configured: true, models: []string{"lookup-model"}}
	providers.Register(prov)

	// No provider in context → must fall back to FindModel on the bare name.
	got, model, err := resolveProviderModel(context.Background(), "lookup-model")
	if err != nil {
		t.Fatalf("resolveProviderModel: %v", err)
	}
	if got != prov {
		t.Errorf("expected looked-up provider, got %v", got)
	}
	if model != "lookup-model" {
		t.Errorf("expected model %q, got %q", "lookup-model", model)
	}
}

func TestResolveProviderModel_NoContextNoMatch(t *testing.T) {
	_, _, err := resolveProviderModel(context.Background(), "totally-unknown-model")
	if err == nil {
		t.Fatal("expected error when no context provider and no registry match")
	}
}
