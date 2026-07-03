package main

import (
	"context"
	"errors"
	"strings"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/display"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
	"github.com/decodo/tyci/tools"
)

func runTUI(initialProvider providers.Provider, initialModelName string, tuiDisp *display.TUI, cfg agent.Config, baseCtx context.Context, sessionPath string) {
	var conversation []providers.RichMessage
	var totalUsage stream.Usage

	// Mutable provider/model that can change via Tab/Shift+Tab
	provider := initialProvider
	modelName := initialModelName

	// Replay session history if resuming
	if cfg.Session != nil && cfg.Session.IsResume() && sessionPath != "" {
		replaySessionToDisplay(tuiDisp, sessionPath)
		parsedLines := cfg.Session.Messages()
		rebuiltMsgs, _ := session.RebuildMessages(parsedLines)
		if len(rebuiltMsgs) > 0 {
			conversation = rebuiltMsgs
		}
	}

	// Close TUI on exit, write session end
	defer func() {
		if cfg.Session != nil {
			agent.WriteSessionEnd(cfg.Session, "ok", 0, &totalUsage)
		}
		tuiDisp.Close()
	}()

	// updateModel resolves a new model string and updates provider/config.
	// This is in-memory only for the current TUI process. It does not write
	// agents/config files, and /new must not reset it.
	updateModel := func(newModel string) {
		p, m, ok := providers.FindModel(newModel)
		if !ok {
			tuiDisp.SetModel(modelName) // revert TUI display to previous model
			return
		}
		provider = p
		modelName = m
		cfg.Model = m
		cfg.ProviderName = p.Name()
	}

	// drainModelChanges applies all queued model changes before running a prompt.
	// This avoids a race where the user picks /model and immediately submits the
	// next prompt while the model change is still buffered.
	drainModelChanges := func() {
		for {
			select {
			case newModel, ok := <-tuiDisp.ModelChanges():
				if !ok {
					return
				}
				updateModel(newModel)
			default:
				return
			}
		}
	}

	for {
		iterCtx, iterCancel := context.WithCancel(baseCtx)

		// Wait for user input or model change
		var line string
		select {
		case newModel, ok := <-tuiDisp.ModelChanges():
			iterCancel()
			if ok {
				updateModel(newModel)
			}
			continue

		case l, ok := <-tuiDisp.Results():
			if !ok {
				iterCancel()
				return
			}
			line = l

		case <-tuiDisp.DoneCh():
			iterCancel()
			return
		}

		drainModelChanges()

		line = strings.TrimSpace(line)
		if line == "/exit" {
			iterCancel()
			return
		}
		if line == "/new" {
			iterCancel()
			conversation = nil
			tools.ClearTodoList()
			tuiDisp.Reset()
			if totalUsage.Input > 0 || totalUsage.Output > 0 {
				tuiDisp.ShowTotalUsage(totalUsage)
			}
			continue
		}
		if line == "" {
			iterCancel()
			continue
		}

		conversation = append(conversation, providers.RichMessage{
			Role:    "user",
			Content: []providers.ContentBlock{{Type: "text", Text: line}},
		})

		if cfg.Session != nil {
			blocks := []session.ContentBlock{{Type: "text", Text: line}}
			_ = cfg.Session.WriteMessage("user", blocks, nil)
		}

		// Run agent in a goroutine so we can interrupt it via ESC
		type agentResult struct {
			usage stream.Usage
			err   error
		}
		resultCh := make(chan agentResult, 1)
		go func() {
			u, e := agent.Run(iterCtx, provider, tuiDisp, &conversation, cfg)
			resultCh <- agentResult{usage: u, err: e}
		}()

		select {
		case <-tuiDisp.CancelCh():
			// ESC pressed — cancel the agent run
			iterCancel()
			res := <-resultCh // wait for agent to finish
			if !errors.Is(res.err, context.Canceled) && res.err != nil {
				// Real error, not just cancellation
			}
			tuiDisp.ResetStatus()
			// User probably wants to retry with a new prompt
			continue

		case res := <-resultCh:
			iterCancel()
			totalUsage.Add(res.usage)

			tuiDisp.Done(res.usage, stream.Stats{})

			if res.err != nil && !errors.Is(res.err, context.Canceled) {
				return
			}
		}
	}
}

// watchESC starts a goroutine that monitors the terminal for the ESC key (0x1b).
// When ESC is pressed, it calls cancel() to interrupt the current operation.
// It sets stdin to raw+cbreak mode (non-canonical, echo off, ISIG on, OPOST on)
// with VMIN=0 and VTIME=1 (100ms timeout) so the goroutine can exit promptly
// when the context is cancelled externally (e.g. Ctrl+C).
// Returns a cleanup function that restores the original terminal state.
// If stdin is not a terminal, returns a no-op function.
