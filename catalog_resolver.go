package main

import (
	"fmt"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/conductor"
	"github.com/decodo/tyci/connector"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/providers"
)

// catalogResolver is the CLI's implementation of conductor.ModelResolver: it
// looks a "provider/model" spec up in the global provider catalog and hands
// back a ready connector.ModelClient.
//
// This is the whole reason the conductor package can exist without importing
// providers. The catalog is a property of the CLI — it is populated from
// providers.json, models.dev and auth.json at startup — and nothing about
// running a conversation needs to know that.
//
// requireConfigured reflects a difference the two interactive frontends have
// always had, now stated once instead of twice. The console's /model refuses
// a provider that has no credentials and says exactly how to add one; the
// TUI's model switcher never checked, because the list it offers is already
// filtered to configured providers and silently rejecting a favorite would
// read as a dead key press.
type catalogResolver struct{ requireConfigured bool }

func (r catalogResolver) Resolve(spec string) (connector.ModelClient, error) {
	p, m, ok := providers.FindModel(spec)
	if !ok {
		return nil, &modelNotFoundError{spec: spec}
	}
	if r.requireConfigured && !p.IsConfigured() {
		return nil, &providerNotConfiguredError{provider: p.Name()}
	}
	return p.Client(m), nil
}

// modelNotFoundError and providerNotConfiguredError are typed so a frontend
// can tell the two failures apart with errors.As and render its own wording.
// The conductor passes the resolver's error through untouched precisely so
// this stays possible.
type modelNotFoundError struct{ spec string }

func (e *modelNotFoundError) Error() string { return fmt.Sprintf("model %q not found", e.spec) }

type providerNotConfiguredError struct{ provider string }

func (e *providerNotConfiguredError) Error() string {
	return fmt.Sprintf("provider %q is not configured", e.provider)
}

// newConductor hands the pieces initCommon produced over to the object that
// owns the conversation from here on. Every frontend goes through it, which
// is what makes "the console and the TUI run the same conversation loop"
// true by construction rather than by review.
//
// WorkDir is deliberately left empty: the conductor then calls os.Getwd() at
// the moment it opens a session file, which is what each frontend used to do
// inline. Capturing it here instead would freeze a directory the process may
// still change.
func newConductor(provider providers.Provider, modelName string, disp display.Display, cfg agent.Config, sessionPath string, resolver conductor.ModelResolver) *conductor.Conductor {
	return conductor.New(conductor.Options{
		Client:      provider.Client(modelName),
		Sink:        disp,
		Config:      cfg,
		Resolver:    resolver,
		SessionPath: sessionPath,
	})
}
