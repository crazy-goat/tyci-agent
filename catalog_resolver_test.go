package main

import (
	"errors"
	"testing"

	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/providers"
)

// catalogResolver is what keeps package conductor free of package providers:
// the conductor changes models through this interface and never sees the
// catalog. These tests pin the two behaviors the frontends rely on.

var _ conductor.ModelResolver = catalogResolver{}

func TestCatalogResolver_ResolvesToProviderClient(t *testing.T) {
	prov := &fakeProvider{name: "resolver-prov", configured: true, models: []string{"m1"}}
	providers.Register(prov)

	mc, err := catalogResolver{}.Resolve("resolver-prov/m1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mc.Provider() != "resolver-prov" || mc.Model() != "m1" {
		t.Errorf("resolved to %s/%s, want resolver-prov/m1", mc.Provider(), mc.Model())
	}
}

// TestCatalogResolver_UnknownModelIsTyped matters because the console prints a
// different message for "no such model" than for "no key for that provider";
// it can only tell them apart if the error survives the conductor untouched.
func TestCatalogResolver_UnknownModelIsTyped(t *testing.T) {
	_, err := catalogResolver{}.Resolve("no-such-prov/no-such-model")
	var notFound *modelNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v (%T), want *modelNotFoundError", err, err)
	}
}

// TestCatalogResolver_RequireConfiguredRejectsUnconfigured covers the
// console's /model: a provider with no credentials is refused, with the
// provider name carried on the error so the caller can say how to add a key.
func TestCatalogResolver_RequireConfiguredRejectsUnconfigured(t *testing.T) {
	prov := &fakeProvider{name: "keyless-prov", configured: false, models: []string{"m1"}}
	providers.Register(prov)

	_, err := catalogResolver{requireConfigured: true}.Resolve("keyless-prov/m1")
	var notConfigured *providerNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("err = %v (%T), want *providerNotConfiguredError", err, err)
	}
	if notConfigured.provider != "keyless-prov" {
		t.Errorf("error carries provider %q, want keyless-prov", notConfigured.provider)
	}
}

// TestCatalogResolver_WithoutRequireConfiguredAcceptsUnconfigured pins the
// deliberate difference: the TUI's model switcher never checked for
// credentials, because its list is already filtered to configured providers
// and a silently ignored Tab would read as a broken key press.
func TestCatalogResolver_WithoutRequireConfiguredAcceptsUnconfigured(t *testing.T) {
	prov := &fakeProvider{name: "keyless-tui-prov", configured: false, models: []string{"m1"}}
	providers.Register(prov)

	mc, err := catalogResolver{}.Resolve("keyless-tui-prov/m1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mc.Model() != "m1" {
		t.Errorf("resolved model = %q, want m1", mc.Model())
	}
}
