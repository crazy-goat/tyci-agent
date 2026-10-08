package main

import (
	"github.com/crazy-goat/tyci-agent/agent"
	"github.com/crazy-goat/tyci-agent/conductor"
	"github.com/crazy-goat/tyci-agent/display"
	"github.com/crazy-goat/tyci-agent/providers"
)

// newConductor hands the pieces initCommon produced over to the object that
// owns the conversation from here on. Every frontend goes through it, which
// is what makes "the TUI and the run mode run the same conversation loop"
// true by construction rather than by review.
//
// WorkDir is deliberately left empty: the conductor then calls os.Getwd() at
// the moment it opens a session file, which is what each frontend used to do
// inline. Capturing it here instead would freeze a directory the process may
// still change.
func newConductor(provider providers.Provider, modelName string, disp display.Display, cfg agent.Config, sessionPath string) *conductor.Conductor {
	c := conductor.New(conductor.Options{
		Client:      provider.Client(modelName),
		Sink:        disp,
		Config:      cfg,
		SessionPath: sessionPath,
	})
	// Wire compaction for every frontend, including one-shot `tyci run`.
	// The callback lazily opens the configured session before writing.
	c.SetCompactor(c.Compact)
	return c
}
