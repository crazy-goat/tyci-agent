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
	replaySessionToDisplay(disp, sessionPath)
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
