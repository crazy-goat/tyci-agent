package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
)

func runPrompt(provider providers.Provider, disp display.Display, prompt string, cfg agent.Config, ctx context.Context, sess *session.Session, sessionPath string) {
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	sigCh, sigDone := watchInterrupt(runCtx, runCancel)
	stopESC := watchESC(runCancel)

	// For one-shot `tyci run --prompt ...` we always have a user prompt,
	// so it's safe — and correct — to materialize the session file here.
	// initCommon leaves sess nil for auto-generated paths and only opens
	// eagerly for explicit --session (resume). For the auto-gen path we
	// need to open the file right before the first write; doing it later
	// (after agent.Run) would skip writing the user line itself.
	wd, _ := os.Getwd()
	opened, _, lerr := ensureLazySession(sess, sessionPath, wd, cfg.Model, provider.Name())
	if lerr == nil && opened != nil {
		sess = opened
		cfg.Session = opened
	}

	messages := buildPromptMessages(prompt, disp, sess, sessionPath)
	writePromptToSession(sess, prompt)

	usage, err := agent.Run(runCtx, provider, disp, &messages, cfg)

	stopESC()
	signal.Stop(sigCh)
	runCancel()
	<-sigDone

	finishPromptRun(disp, sess, sessionPath, usage, err)
}

func watchInterrupt(ctx context.Context, cancel context.CancelFunc) (chan os.Signal, chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	sigDone := make(chan struct{})
	go func() {
		defer close(sigDone)
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	return sigCh, sigDone
}

func buildPromptMessages(prompt string, disp display.Display, sess *session.Session, sessionPath string) []providers.RichMessage {
	messages := []providers.RichMessage{{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: prompt}},
	}}
	if sess == nil || !sess.IsResume() {
		return messages
	}
	parsedLines := sess.Messages()
	rebuiltMsgs, err := session.RebuildMessages(parsedLines)
	if err != nil || len(rebuiltMsgs) == 0 {
		return messages
	}
	fmt.Fprintf(os.Stderr, "ℹ Resumed session %s (%d messages) from %s\n", sess.ID(), len(rebuiltMsgs), sessionPath)
	// One-shot run mode: render a compact summary (no full replay). A
	// huge session would otherwise flood the terminal and bury the
	// prompt output in stale scrolling text. The model still sees
	// `rebuiltMsgs` so context is preserved.
	summary, _, total, corrupt, err := session.LoadForReplay(sessionPath)
	if err == nil {
		summarizeResume(disp, summary.ID, rebuiltMsgs, total, len(corrupt))
	} else {
		fmt.Fprintf(os.Stderr, "Warning: cannot summarize session: %v\n", err)
	}
	return append(rebuiltMsgs, providers.RichMessage{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: prompt}},
	})
}

func writePromptToSession(sess *session.Session, prompt string) {
	if sess == nil || prompt == "" {
		return
	}
	blocks := []session.ContentBlock{{Type: "text", Text: prompt}}
	_ = sess.WriteMessage("user", blocks, nil)
}
