package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/decodo/tyci/agent"
)

// runAgentIteration hands one user line to the conductor and renders the
// outcome. Arming SIGINT and ESC is the console's job — it owns the terminal
// — while what "interrupted" and "failed" look like on screen is a rendering
// decision that deliberately stayed here too.
func (s *interactiveState) runAgentIteration(iterCtx context.Context, iterCancel context.CancelFunc, line string) bool {
	watchCtx, stopWatch := context.WithCancel(context.Background())
	sigCh, sigDone := s.startInterruptWatcher(watchCtx, iterCancel)
	stopESC := watchESC(iterCancel)
	_, err := s.cond.Submit(iterCtx, line)
	stopESC()
	s.stopInterruptWatcher(sigCh, sigDone, iterCancel, stopWatch)

	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.display.End()
			fmt.Fprint(os.Stdout, "\n")
			return false
		}
		// The iteration-cap warning is already shown by agent.Run; treat it as
		// a graceful stop rather than surfacing it as an error to the user.
		if !errors.Is(err, agent.ErrMaxIterations) {
			s.display.End()
			fmt.Fprintf(os.Stdout, "Error: %v\n", err)
			return false
		}
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
		for {
			select {
			case <-sigCh:
				cancel()
			case <-ctx.Done():
				return
			}
		}
	}()
	return sigCh, sigDone
}

func (s *interactiveState) stopInterruptWatcher(sigCh chan os.Signal, sigDone chan struct{}, cancel context.CancelFunc, stopWatch context.CancelFunc) {
	signal.Stop(sigCh)
	stopWatch()
	cancel()
	<-sigDone
}
