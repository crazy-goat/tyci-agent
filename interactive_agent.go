package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/decodo/tyci/agent"
	"github.com/decodo/tyci/providers"
	"github.com/decodo/tyci/session"
	"github.com/decodo/tyci/stream"
)

func (s *interactiveState) submitUserLine(line string) {
	if s.editor != nil {
		s.editor.AddHistory(line)
	}
	s.conversation = append(s.conversation, providers.RichMessage{
		Role:    "user",
		Content: []providers.ContentBlock{{Type: "text", Text: line}},
	})
	if s.cfg.Session != nil {
		blocks := []session.ContentBlock{{Type: "text", Text: line}}
		_ = s.cfg.Session.WriteMessage("user", blocks, nil)
	}
}

func (s *interactiveState) runAgentIteration(iterCtx context.Context, iterCancel context.CancelFunc) bool {
	sigCh, sigDone := s.startInterruptWatcher(iterCtx, iterCancel)
	stopESC := watchESC(iterCancel)
	usage, err := agent.Run(iterCtx, s.provider, s.display, &s.conversation, s.cfg)
	stopESC()
	s.stopInterruptWatcher(sigCh, sigDone, iterCancel)
	s.addUsage(usage)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.display.End()
			fmt.Fprint(os.Stdout, "\n")
			return false
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return true
	}
	s.display.End()
	fmt.Fprint(os.Stdout, "\n")
	return false
}

func (s *interactiveState) startInterruptWatcher(ctx context.Context, cancel context.CancelFunc) (chan os.Signal, chan struct{}) {
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

func (s *interactiveState) stopInterruptWatcher(sigCh chan os.Signal, sigDone chan struct{}, cancel context.CancelFunc) {
	signal.Stop(sigCh)
	cancel()
	<-sigDone
}

func (s *interactiveState) addUsage(usage stream.Usage) {
	s.totalUsage.Add(usage)
}
